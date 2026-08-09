#!/usr/bin/env bash
# Controls on scripts/revision-probe.sh — the EXTERNAL witness that the redeploy
# reached the daemon (mg-ce10).
#
# THE LOAD-BEARING CASE IS SECTION 5, and everything else is here to make its
# verdict interpretable. The rule this file guards is:
#
#     A detector for "X did not happen" must not be ACTIVATED BY X.
#
# driftwatch (mg-5bd2) reports the running daemon's revision age correctly and
# ships entirely inside pogod — which the redeploy installs. So on a night the
# redeploy fails, the alarm for the failed redeploy is dark. This probe is the
# independent witness, and it is only independent for as long as it touches
# nothing the deploy provides. Section 5 puts POISONED `go`, `pogo`, `pogod` and
# `jq` first on PATH and asserts the probe still reaches its verdict and that no
# marker was written. An exit-code-only assertion would pass against a probe that
# shelled out to a `pogo` that happened to agree.
#
# Section 6 is the second-order version of the same defect and the reason this
# probe does not simply run `git rev-parse origin/main`: a remote-tracking ref is
# refreshed by a fetch, and in deploy-src the thing that fetches is the deploy
# runner. On a night the deploy never fires, that ref does not advance either, so
# a probe keyed to it compares two stale numbers, finds them equal and reports
# health. The section stages exactly that state and asserts the two ref sources
# DISAGREE — the local one says OK, the default says diverged.
#
# The daemon is a stub HTTP server rather than a real pogod, deliberately: the
# probe's subject is a revision string it cannot control, and a real daemon can
# only ever report the one revision the test binary was built from. Every case
# below needs a different one.

set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# shellcheck source=/dev/null
source "$HERE/pogo-sandbox"

RESULTS_FILE=$(mktemp)
pogo_sandbox_create revprobe
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

PROBE="$HERE/revision-probe.sh"
[ -x "$PROBE" ] || pogo_sandbox_fail "scripts/revision-probe.sh is not executable — the thing under test cannot be run"

# ---------------------------------------------------------------------------
# Fixtures: a bare upstream, and two clones taken at DIFFERENT points
# ---------------------------------------------------------------------------
# FRESH holds every object and a current origin/main. STALE was cloned one
# commit earlier and has never fetched since — it is the deploy-src of a box
# whose deploy has not run, which is section 6's whole subject.

export GIT_AUTHOR_NAME=probe GIT_AUTHOR_EMAIL=probe@example.com
export GIT_COMMITTER_NAME=probe GIT_COMMITTER_EMAIL=probe@example.com

UPSTREAM="$SANDBOX/upstream.git"
SEED="$SANDBOX/seed"
git init --bare --quiet "$UPSTREAM" || pogo_sandbox_fail "could not create the bare upstream fixture"
git clone --quiet "$UPSTREAM" "$SEED" 2>/dev/null || pogo_sandbox_fail "could not clone the upstream fixture"
git -C "$SEED" symbolic-ref HEAD refs/heads/main

mkcommit() {
    echo "$1" > "$SEED/file"
    git -C "$SEED" add file
    git -C "$SEED" commit --quiet -m "$1"
}
mkcommit one; mkcommit two
git -C "$SEED" push --quiet origin main 2>/dev/null || pogo_sandbox_fail "could not push the fixture history"
C1="$(git -C "$SEED" rev-parse HEAD~1)"
C2="$(git -C "$SEED" rev-parse HEAD)"

STALE="$SANDBOX/checkout-stale"
git clone --quiet "$UPSTREAM" "$STALE" 2>/dev/null || pogo_sandbox_fail "could not clone the stale checkout fixture"

mkcommit three
git -C "$SEED" push --quiet origin main 2>/dev/null || pogo_sandbox_fail "could not push the fixture tip"
C3="$(git -C "$SEED" rev-parse HEAD)"

FRESH="$SANDBOX/checkout-fresh"
git clone --quiet "$UPSTREAM" "$FRESH" 2>/dev/null || pogo_sandbox_fail "could not clone the fresh checkout fixture"

[ "$(git -C "$STALE" rev-parse refs/remotes/origin/main)" = "$C2" ] \
    || pogo_sandbox_fail "the stale checkout's origin/main is not at C2 — section 6's premise does not hold"
[ "$(git -C "$FRESH" rev-parse refs/remotes/origin/main)" = "$C3" ] \
    || pogo_sandbox_fail "the fresh checkout's origin/main is not at C3"

# ---------------------------------------------------------------------------
# Fixture: the stub daemon
# ---------------------------------------------------------------------------
# Serves whatever is in $VERSION_FILE at request time, so a case changes the
# running revision by writing a file. It binds port 0 and reports the port it
# got: a hard-coded port is a control that fails on a busy box and blames the
# branch.

VERSION_FILE="$SANDBOX/version.json"
PORT_FILE="$SANDBOX/stub.port"

command -v python3 >/dev/null 2>&1 \
    || pogo_sandbox_fail "python3 is absent — the stub daemon cannot be stood up, and no assertion below would mean anything without one"

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

serve_revision() { printf '{"revision":"%s","go_version":"go1.25.0"}\n' "$1" > "$VERSION_FILE"; }
serve_revision "$C3"

python3 "$SANDBOX/stub.py" "$VERSION_FILE" > "$PORT_FILE" 2>/dev/null &
STUB_PID=$!
for _ in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20; do
    PORT="$(head -1 "$PORT_FILE" 2>/dev/null)"
    [ -z "$PORT" ] || break
    sleep 0.25
done
[ -n "${PORT:-}" ] || pogo_sandbox_fail "the stub daemon never reported a port"
URL="http://127.0.0.1:$PORT"

STAMP="$SANDBOX/probe.stamp"
# A fixed clock, so nothing below depends on how long the suite takes to run.
T0=1754000000                       # the "first diverged" instant every stamp uses
NOW_YOUNG=$(( T0 + 3600 ))          # 1h later  — inside a 24h threshold
NOW_OLD=$(( T0 + 2 * 86400 ))       # 2d later  — past it

run_probe() { bash "$PROBE" --url "$URL" --stamp "$STAMP" --tries 1 "$@" 2>&1; }

# --- 1. OK — the daemon is on the reference -----------------------------------
serve_revision "$C3"
rm -f "$STAMP"
printf '%s %s %s\n' "$T0" "$C3" "$C3" > "$STAMP"
out="$(run_probe --repo "$FRESH" --now "$NOW_OLD" --stale-after 24h)"; rc=$?
if [ "$rc" -eq 0 ]; then
    pass "probe exits 0 when the running revision IS the reference"
else
    fail "probe exited $rc on a current daemon, want 0 — output: $out"
fi
if [ -f "$STAMP" ]; then
    fail "the stamp survived a clean run — the next divergence would be timed from a divergence that has already closed"
else
    pass "a clean run clears the divergence stamp"
fi

# --- 2. ALERT — diverged for longer than N, and it names the commit gap -------
serve_revision "$C1"
printf '%s %s %s\n' "$T0" "$C1" "$C3" > "$STAMP"
out="$(run_probe --repo "$FRESH" --now "$NOW_OLD" --stale-after 24h)"; rc=$?
if [ "$rc" -eq 1 ]; then
    pass "probe exits 1 when the running revision has differed from the reference for longer than N"
else
    fail "probe exited $rc on a 2-day divergence with a 24h threshold, want 1 — output: $out"
fi
if printf '%s' "$out" | grep -q '2 commit(s) between the running revision and the reference'; then
    pass "the alert names the gap in commits"
else
    fail "the alert did not name the 2-commit gap — output: $out"
fi
if printf '%s' "$out" | grep -q "$C1" && printf '%s' "$out" | grep -q "$C3"; then
    pass "the alert names both revisions it compared"
else
    fail "the alert did not name both revisions — output: $out"
fi

# --- 3. QUIET — the same divergence, younger than N --------------------------
# Same fixture, same stamp, a different clock. A threshold that fires on any
# divergence at all would fire every night between the merge and the 03:00
# deploy, and an alarm that is always on is an alarm nobody reads.
out="$(run_probe --repo "$FRESH" --now "$NOW_YOUNG" --stale-after 24h)"; rc=$?
if [ "$rc" -eq 0 ]; then
    pass "probe stays quiet while the divergence is younger than N"
else
    fail "probe exited $rc on a 1-hour divergence with a 24h threshold, want 0 — output: $out"
fi

# --- 4. The clock is keyed on the RUNNING revision ----------------------------
# Two directions, and they are the whole reason the stamp exists:
#
#   (a) a NEW running revision restarts it — a deploy happened, which is the
#       event this probe watches for. It may still be behind a main that moved
#       since, and that is not the failure being watched.
#   (b) main advancing does NOT restart it — main advances all day, and a clock
#       it could reset would leave the alarm permanently disarmed on a busy repo.
serve_revision "$C2"
printf '%s %s %s\n' "$T0" "$C1" "$C3" > "$STAMP"     # stamped against a DIFFERENT running rev
out="$(run_probe --repo "$FRESH" --now "$NOW_OLD" --stale-after 24h)"; rc=$?
if [ "$rc" -eq 0 ]; then
    pass "a changed running revision restarts the divergence clock — a deploy DID happen"
else
    fail "probe exited $rc after the running revision advanced, want 0 — output: $out"
fi

serve_revision "$C1"
printf '%s %s %s\n' "$T0" "$C1" "0000000000000000000000000000000000000000" > "$STAMP"
out="$(run_probe --repo "$FRESH" --now "$NOW_OLD" --stale-after 24h)"; rc=$?
if [ "$rc" -eq 1 ]; then
    pass "a moved reference does NOT restart the clock — only the running revision does"
else
    fail "probe exited $rc when only the reference had moved, want 1 — output: $out"
fi

# --- 5. IT MUST NOT TOUCH ANYTHING THE DEPLOY INSTALLS ------------------------
# The load-bearing control. `go`, `pogo` and `pogod` are all installed by the
# redeploy, so a probe that reaches for any of them is armed only by a redeploy
# that worked — the exact defect this ticket exists to fix, reintroduced. `jq` is
# poisoned too: it is not deploy-installed, but it is not universal either, and a
# probe that exits 2 on a box without it is dark for a reason unrelated to the
# fault it watches.
#
# Both halves are asserted. The marker alone would miss a probe that shelled out
# and ignored the answer; the exit status alone passes against a fallback that
# happens to agree.
POISON="$SANDBOX/poison"
mkdir -p "$POISON"
for tool in go pogo pogod jq; do
    cat > "$POISON/$tool" <<EOF
#!/usr/bin/env bash
echo "$tool was invoked" > "$POISON/$tool.marker"
exit 66
EOF
    chmod +x "$POISON/$tool"
done
rm -f "$POISON"/*.marker

serve_revision "$C1"
printf '%s %s %s\n' "$T0" "$C1" "$C3" > "$STAMP"
out="$(PATH="$POISON:$PATH" bash "$PROBE" --url "$URL" --stamp "$STAMP" --tries 1 \
        --repo "$FRESH" --now "$NOW_OLD" --stale-after 24h 2>&1)"; rc=$?
invoked=""
for tool in go pogo pogod jq; do
    [ -f "$POISON/$tool.marker" ] && invoked="$invoked $tool"
done
if [ -n "$invoked" ]; then
    fail "the probe invoked$invoked — a witness that reaches for what the deploy installs is armed only by a deploy that worked, which is mg-ce10 verbatim"
else
    pass "the probe invoked none of go/pogo/pogod/jq — it is armed by the MERGE, not by the deploy"
fi
if [ "$rc" -eq 66 ]; then
    fail "the probe returned a poisoned binary's exit status ($rc)"
elif [ "$rc" -eq 1 ]; then
    pass "the probe reached its own verdict with the deploy's artifacts poisoned on PATH"
else
    fail "the probe exited $rc with go/pogo/pogod/jq poisoned, want 1 — output: $out"
fi

# --- 6. The reference is read from the REMOTE, not from a fetched-at-deploy ref
# The second-order version of the same defect. STALE's origin/main still points
# at C2 because nothing has fetched it since — which is the state of deploy-src
# on a box whose deploy has not run. A daemon stuck on C2 then matches it
# exactly, and a probe keyed to the local ref reports health while two stale
# numbers agree with each other.
serve_revision "$C2"
printf '%s %s %s\n' "$T0" "$C2" "$C3" > "$STAMP"
local_out="$(run_probe --repo "$STALE" --ref-source local --now "$NOW_OLD" --stale-after 24h)"; local_rc=$?
if [ "$local_rc" -eq 0 ]; then
    pass "the local-ref reading reports OK against a stale checkout — the hole is real and is reproduced here"
else
    fail "the local-ref control did not reproduce the hole (exit $local_rc) — section 6 proves nothing if it cannot — output: $local_out"
fi

printf '%s %s %s\n' "$T0" "$C2" "$C3" > "$STAMP"
remote_out="$(run_probe --repo "$STALE" --now "$NOW_OLD" --stale-after 24h)"; remote_rc=$?
if [ "$remote_rc" -eq 1 ]; then
    pass "the DEFAULT reading catches it: ls-remote sees the real tip and the probe alerts"
else
    fail "the default reading exited $remote_rc against a stale checkout, want 1 — the probe would inherit the deploy's own staleness — output: $remote_out"
fi
if printf '%s' "$remote_out" | grep -q 'ls-remote'; then
    pass "the report names where the reference came from"
else
    fail "the report did not name the reference source — a reader cannot tell a network read from a stale local one — output: $remote_out"
fi

# --- 7. A silent daemon is a FINDING, never a clean bill of health ------------
# Port 1 refuses instantly. The failure mode being excluded is the one this whole
# lineage is about: absence of evidence read as evidence of health.
out="$(bash "$PROBE" --url http://127.0.0.1:1 --stamp "$STAMP" --tries 1 \
        --repo "$FRESH" --now "$NOW_OLD" --stale-after 24h 2>&1)"; rc=$?
if [ "$rc" -eq 2 ]; then
    pass "an unreachable daemon exits 2 — reported, and reported as distinct from a stale revision"
else
    fail "probe exited $rc against a dead port, want 2 — output: $out"
fi
if [ "$rc" -eq 0 ]; then
    fail "a daemon that never answered was reported as healthy"
fi

# --- 8. Unreachable and unstamped are DIFFERENT states -----------------------
# One owes a restart, the other owes an investigation. Collapsing them into a
# single shrug is mg-de08 in miniature.
printf '{"go_version":"go1.25.0","start_time":"2026-08-07T00:00:00Z"}\n' > "$VERSION_FILE"
out="$(run_probe --repo "$FRESH" --now "$NOW_OLD" --stale-after 24h)"; rc=$?
if [ "$rc" -eq 2 ]; then
    pass "a daemon that answers without naming a revision exits 2"
else
    fail "probe exited $rc against a daemon reporting no revision, want 2 — output: $out"
fi
if printf '%s' "$out" | grep -q 'answered but named no revision'; then
    pass "the unstamped-daemon report is distinguishable from the unreachable one"
else
    fail "the unstamped daemon was reported with the unreachable wording — output: $out"
fi

# --- 9. It resolves its checkout from its own path, not from $PWD ------------
# A schedule fires from whatever directory it likes. A probe that resolved its
# repo against $PWD would work in every hand-run and be dark under the schedule
# that actually arms it.
serve_revision "$C1"
printf '%s %s %s\n' "$T0" "$C1" "$C3" > "$STAMP"
out="$(cd / && bash "$PROBE" --url "$URL" --stamp "$STAMP" --tries 1 \
        --repo "$FRESH" --now "$NOW_OLD" --stale-after 24h 2>&1)"; rc=$?
if [ "$rc" -eq 1 ]; then
    pass "the probe works when invoked from an unrelated directory"
else
    fail "probe exited $rc when run from / — output: $out"
fi

# --- 10. A first divergence records its own start ----------------------------
# With no stamp at all the probe must NOT alert on the spot — it has not yet
# observed the condition for N — but it must write the stamp, or the clock
# restarts on every run and the alarm can never mature.
rm -f "$STAMP"
out="$(run_probe --repo "$FRESH" --now "$NOW_OLD" --stale-after 24h)"; rc=$?
if [ "$rc" -eq 0 ]; then
    pass "a first-seen divergence does not alert on the spot"
else
    fail "probe exited $rc on a divergence it had never seen before, want 0 — output: $out"
fi
if [ -f "$STAMP" ] && grep -q "$C1" "$STAMP"; then
    pass "a first-seen divergence is recorded, so the clock can mature"
else
    fail "no stamp was written — the divergence clock would restart every run and the alarm could never fire"
fi

# --- 11. IT REPORTS EITHER WAY — the ledger (mg-a03d) -------------------------
# The arming half of this lineage. A witness that writes only when it is unhappy
# cannot be told apart from a witness that is not running, and "no alert" then
# means both "healthy" and "dark". Every case below asserts a line was written
# AND that it names the verdict, because a ledger that records only that
# something happened is the same shrug one layer along.
#
# The GREEN arm is the one that is easy to skip and the one that matters: the
# red arm is exercised by every other section here, and a check that has only
# ever produced a red is a check of unknown polarity.

LEDGER="$SANDBOX/ledger.log"

ledger_lines() { [ -f "$LEDGER" ] && wc -l < "$LEDGER" | tr -d ' ' || echo 0; }

rm -f "$LEDGER"
serve_revision "$C3"
rm -f "$STAMP"
run_probe --repo "$FRESH" --now "$NOW_OLD" --stale-after 24h --log "$LEDGER" >/dev/null 2>&1
if [ "$(ledger_lines)" = "1" ] && grep -q ' OK ' "$LEDGER" && grep -q 'exit=0' "$LEDGER"; then
    pass "a GREEN run writes a ledger line — silence and health stop being the same observation"
else
    fail "no OK ledger line after a clean run — got: $(cat "$LEDGER" 2>&1)"
fi

serve_revision "$C1"
printf '%s %s %s\n' "$T0" "$C1" "$C3" > "$STAMP"
run_probe --repo "$FRESH" --now "$NOW_OLD" --stale-after 24h --log "$LEDGER" >/dev/null 2>&1
if [ "$(ledger_lines)" = "2" ] && grep -q ' ALERT ' "$LEDGER" && grep -q 'exit=1' "$LEDGER"; then
    pass "a RED run appends its own ledger line, naming the ALERT verdict"
else
    fail "no ALERT ledger line after an alerting run — got: $(cat "$LEDGER" 2>&1)"
fi

printf '%s %s %s\n' "$T0" "$C1" "$C3" > "$STAMP"
run_probe --repo "$FRESH" --now "$NOW_YOUNG" --stale-after 24h --log "$LEDGER" >/dev/null 2>&1
if grep -q ' DIVERGED ' "$LEDGER"; then
    pass "a within-threshold divergence is DIVERGED in the ledger, not folded into OK"
else
    fail "the within-threshold run did not record a distinct verdict — got: $(cat "$LEDGER" 2>&1)"
fi

# A probe that dies on its own setup must still leave a line saying it tried.
# This is the case the EXIT trap exists for: it is the rarest path and the one a
# per-branch log call gets added without.
before="$(ledger_lines)"
bash "$PROBE" --url http://127.0.0.1:1 --stamp "$STAMP" --tries 1 \
    --repo "$FRESH" --now "$NOW_OLD" --stale-after 24h --log "$LEDGER" >/dev/null 2>&1
if [ "$(ledger_lines)" -gt "$before" ] && grep -q 'UNREACHABLE' "$LEDGER"; then
    pass "a probe that could not reach the daemon records that it could not — an exit-2 run is not a missing run"
else
    fail "the unreachable-daemon path wrote no ledger line, so a probe that cannot reach the daemon is indistinguishable from one that never fired — got: $(cat "$LEDGER" 2>&1)"
fi

# The ledger keeps apart the two states the prose keeps apart. If it did not,
# the distinction this file argues for would live only in a narrative nobody
# reads at 08:00 and not in the record they do.
before="$(ledger_lines)"
printf '{"go_version":"go1.25.0"}\n' > "$VERSION_FILE"
run_probe --repo "$FRESH" --now "$NOW_OLD" --stale-after 24h --log "$LEDGER" >/dev/null 2>&1
if [ "$(ledger_lines)" -gt "$before" ] && grep -q 'NO-REVISION' "$LEDGER"; then
    pass "an unstamped daemon and an unreachable one are DIFFERENT verdicts in the ledger"
else
    fail "the unstamped-daemon run did not record its own verdict — one owes a restart and the other an investigation — got: $(cat "$LEDGER" 2>&1)"
fi

# A refused command line is the one path the trap cannot cover, because the
# ledger's own path arrives on that command line. Asserted so the gap is a
# recorded decision and not a surprise.
before="$(ledger_lines)"
bash "$PROBE" --log "$LEDGER" --no-such-flag >/dev/null 2>&1
if [ "$(ledger_lines)" -eq "$before" ]; then
    pass "an unparseable command line writes no ledger line — the one uncovered path, and it is uncovered on purpose"
else
    fail "an unparseable command line wrote a ledger line; harmless, but the header says it does not"
fi

if grep -q 'running=' "$LEDGER" && grep -q 'reference=' "$LEDGER" && grep -q 'threshold=' "$LEDGER"; then
    pass "the ledger names the two revisions it compared and the threshold it used"
else
    fail "the ledger lines carry no evidence, only a verdict — got: $(cat "$LEDGER" 2>&1)"
fi

# The ledger is a HEARTBEAT, so its timestamp must be the real instant the run
# happened. --now injects a synthetic clock for the age arithmetic; if it also
# moved the ledger timestamp, a back-dated run would look like a missed one.
today="$(date -u +%Y-%m-%d)"
if grep -q "^$today" "$LEDGER"; then
    pass "the ledger timestamps runs in real wall-clock time, not the injected --now"
else
    fail "the ledger timestamp followed --now — a heartbeat a flag can back-date is not a heartbeat — got: $(head -1 "$LEDGER" 2>&1)"
fi

# --- 12. The alert is THROTTLED, and a failed send is not a notification ------
# Hourly sampling is what makes a 24h threshold mean 24 hours (a daily probe
# first SEES a divergence a day late). The cost is 24 identical notifications a
# day for one unchanged fact, which is the "alarm nobody reads" the threshold
# itself exists to prevent. The throttle lives in the probe rather than the
# schedule because sample rate and notify rate answer different questions.
MAILBIN="$SANDBOX/mailbin"
mkdir -p "$MAILBIN"
cat > "$MAILBIN/mg" <<EOF
#!/usr/bin/env bash
# Self-identifies as macguffin, which the probe requires before trusting an mg.
case "\$1" in --help) echo "macguffin — the mg CLI"; exit 0 ;; esac
echo "\$*" >> "$SANDBOX/mail.sent"
exit 0
EOF
chmod +x "$MAILBIN/mg"

mails() { [ -f "$SANDBOX/mail.sent" ] && wc -l < "$SANDBOX/mail.sent" | tr -d ' ' || echo 0; }

rm -f "$SANDBOX/mail.sent"
serve_revision "$C1"
printf '%s %s %s\n' "$T0" "$C1" "$C3" > "$STAMP"
PATH="$MAILBIN:$PATH" run_probe --repo "$FRESH" --now "$NOW_OLD" --stale-after 24h --mail >/dev/null 2>&1
if [ "$(mails)" = "1" ]; then
    pass "the first alerting run mails"
else
    fail "the first alerting run sent $(mails) mail(s), want 1"
fi
if awk '{print $4}' "$STAMP" | grep -q "^$NOW_OLD$"; then
    pass "a delivered notification is recorded in the stamp, keyed to this running revision"
else
    fail "the stamp did not record the notification — the throttle has nothing to read — stamp: $(cat "$STAMP")"
fi

PATH="$MAILBIN:$PATH" run_probe --repo "$FRESH" --now $(( NOW_OLD + 3600 )) --stale-after 24h --mail >/dev/null 2>&1
if [ "$(mails)" = "1" ]; then
    pass "the same unresolved alert an hour later does NOT mail again"
else
    fail "the throttle did not hold: $(mails) mails after a second run one hour on"
fi

out="$(PATH="$MAILBIN:$PATH" run_probe --repo "$FRESH" --now $(( NOW_OLD + 3600 )) --stale-after 24h --mail)"; rc=$?
if [ "$rc" -eq 1 ]; then
    pass "a suppressed notification does not suppress the ALERT or its exit status"
else
    fail "a throttled run exited $rc, want 1 — the throttle changed the verdict, not just the delivery"
fi
if printf '%s' "$out" | grep -qi 'suppressed'; then
    pass "the run says out loud that it suppressed a notification"
else
    fail "the suppression is silent, so a reader cannot tell it from a probe that decided not to alert — output: $out"
fi

PATH="$MAILBIN:$PATH" run_probe --repo "$FRESH" --now $(( NOW_OLD + 13 * 3600 )) --stale-after 24h --mail >/dev/null 2>&1
if [ "$(mails)" = "2" ]; then
    pass "the alert is repeated once the re-notify interval has passed — throttled is not silenced"
else
    fail "$(mails) mails after 13h with a 12h re-notify, want 2 — a throttle that never re-fires is a mute button"
fi

# A CHANGED RUNNING REVISION IS A NEW SITUATION. The throttle is keyed on the
# running revision for the same reason the clock is: a deploy landed, so the
# next divergence deserves its own notification rather than inheriting the
# silence of the last one.
serve_revision "$C2"
printf '%s %s %s %s\n' "$T0" "$C1" "$C3" "$NOW_OLD" > "$STAMP"
# Two runs, because a new running revision restarts the clock by design: the
# first records C2's own divergence start, the second lets it mature. Anything
# that alerted on the first run would be alerting on a deploy that just landed.
PATH="$MAILBIN:$PATH" run_probe --repo "$FRESH" --now "$NOW_OLD" --stale-after 24h --mail >/dev/null 2>&1
if [ "$(mails)" = "2" ]; then
    pass "the run right after a new revision lands does not mail — the clock restarted, which is the point of restarting it"
else
    fail "$(mails) mails on the first run after a deploy landed, want 2 — the probe alerted on a deploy that worked"
fi
PATH="$MAILBIN:$PATH" run_probe --repo "$FRESH" --now $(( NOW_OLD + 25 * 3600 )) --stale-after 24h --mail >/dev/null 2>&1
if [ "$(mails)" = "3" ]; then
    pass "a new running revision resets the notification throttle — a deploy happened, so this is news"
else
    fail "$(mails) mails after the running revision changed, want 3 — the throttle outlived the situation it was throttling"
fi

# A SEND THAT FAILED IS NOT A NOTIFICATION. Recording the attempt would buy
# twelve hours of silence for an alert that reached nobody.
FAILBIN="$SANDBOX/failbin"
mkdir -p "$FAILBIN"
cat > "$FAILBIN/mg" <<'EOF'
#!/usr/bin/env bash
case "$1" in --help) echo "macguffin — the mg CLI"; exit 0 ;; esac
exit 4
EOF
chmod +x "$FAILBIN/mg"
serve_revision "$C1"
printf '%s %s %s\n' "$T0" "$C1" "$C3" > "$STAMP"
PATH="$FAILBIN:$PATH" run_probe --repo "$FRESH" --now "$NOW_OLD" --stale-after 24h --mail >/dev/null 2>&1
if awk '{print $4}' "$STAMP" | grep -q '^-$'; then
    pass "a FAILED send is not recorded as a notification — the next run tries again"
else
    fail "a failed send was stamped as delivered, buying silence for an alert nobody received — stamp: $(cat "$STAMP")"
fi

# --- tally -------------------------------------------------------------------
echo
echo "=== scripts/revision-probe.sh controls ==="
cat "$RESULTS_FILE"
fails=$(grep -c '^FAIL:' "$RESULTS_FILE" || true)
passes=$(grep -c '^PASS:' "$RESULTS_FILE" || true)
echo "$passes passed, $fails failed"
[ "$fails" -eq 0 ] || exit 1
[ "$passes" -gt 0 ] || { echo "no assertions ran"; exit 1; }
