#!/usr/bin/env bash
# Controls on scripts/pogo-sandbox — the packaged test isolation (mg-78a5).
#
# WHY THIS FILE EXISTS
# --------------------
# scripts/pogo-sandbox is the answer to FOUR tickets filed for one defect —
# mg-6092, mg-e8e7, mg-5336 and mg-3412, each a test reading the developer's live
# ~/.pogo, live daemon or live fleet. Each was fixed by hand at its own call
# site, and each left the next instance available. A harness is only worth
# anything if it fails when the isolation fails, so this file BREAKS EACH
# GUARANTEE ON PURPOSE and reads back what comes out.
#
# The negative half is the important one, and it is the half that is easy to skip.
# It is not enough that a broken sandbox stops: the whole of mg-3412 was that a
# broken sandbox stopped WITH THE VOCABULARY OF A REGRESSION. Fourteen assertion
# failures — including verbatim fail-open security findings — were printed about
# a tree that was provably fine, and a coordinator spent fifteen minutes reaching
# the wrong conclusion from them. So every break below is checked twice: that it
# is refused, and that the refusal is shaped like INFRASTRUCTURE (exit 99, a
# SETUP FAILURE banner, no FAIL:/PASS:/PROVED: line) rather than like a verdict.
#
# The positive half is here too (§1), and must stay. A guard observed only
# refusing is as untested as one observed only passing: a pogo_sandbox_isolate
# hard-wired to fail would satisfy every other control in this file while making
# the whole suite unrunnable.
#
# WHAT IS NOT ASSERTED HERE
# -------------------------
# "A broken sandbox daemon binary produces a setup failure end to end" already
# has a control: scripts/pogo-self-deploy_live_setup_test.sh, which boots the
# real live suite against a pogod that dies instantly. This file does not repeat
# it. §6 covers the direction that file cannot reach — a daemon that comes up
# perfectly into a HOME that is not private — because that is the failure the
# other three tickets were, and it is the one that leaves damage behind rather
# than just a red run.

set -u
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$HERE/.." && pwd)"

SETUP_RC=99   # scripts/lib/sandbox-daemon.sh's SANDBOX_SETUP_RC default

WORK="$(mktemp -d)"
RESULTS="$(mktemp)"
cleanup() { chmod -R u+w "$WORK" 2>/dev/null; rm -rf "$WORK"; rm -f "$RESULTS"; }
trap cleanup EXIT

# Guard the WRITE, not the count — a tally drawn from an unwritable ledger
# reports "0 failed" and cannot tell that from "I could not record anything".
pass() { echo "PASS: $1"; echo "PASS: $1" >> "$RESULTS" || { echo "LEDGER WRITE FAILED"; exit 1; }; }
fail() { echo "FAIL: $1"; echo "FAIL: $1" >> "$RESULTS" || { echo "LEDGER WRITE FAILED"; exit 1; }; }

echo "Building pogod for the sandbox controls..."
if ! (cd "$REPO_ROOT" && go build -o "$WORK/pogod" ./cmd/pogod); then
    echo "could not build cmd/pogod — the controls cannot run"; exit 1
fi

# run_child SCRIPT_BODY [ENV_ASSIGNMENTS...] — run a snippet that sources the
# harness, in its own process, capturing output and rc.
#
# Its own process on purpose: every guarantee here ends the run with `exit`, and
# a control that ran them in-process would take the exit with it. This is the
# same reason the harness itself uses globals rather than command substitution.
CHILD_OUT="$WORK/child.out"
CHILD_RC=0
run_child() {
    local body="$1"; shift
    printf '%s\n' "set -u" "source \"$REPO_ROOT/scripts/pogo-sandbox\"" "$body" > "$WORK/child.sh"
    CHILD_RC=0
    env "$@" bash "$WORK/child.sh" > "$CHILD_OUT" 2>&1 || CHILD_RC=$?
}

# assert_setup_failure DESC — the refusal must be shaped like infrastructure.
# Three separate facts, because mg-3412 failed all three at once: the exit code
# has to be distinguishable from an assertion tally, the banner has to say so in
# words, and there must be no assertion-shaped line anywhere in the output for a
# reader to mistake for a finding.
assert_setup_failure() {
    local desc="$1" bad
    [ "$CHILD_RC" = "$SETUP_RC" ] \
        && pass "$desc: exits $SETUP_RC (the SETUP code), not 1 (the assertion-tally code)" \
        || fail "$desc: exited $CHILD_RC, expected $SETUP_RC — a caller still cannot tell 'it never ran' from 'it ran and found something'"
    grep -q "SETUP FAILURE" "$CHILD_OUT" \
        && pass "$desc: prints the SETUP FAILURE banner" \
        || fail "$desc: printed no 'SETUP FAILURE' banner; output was: $(head -8 "$CHILD_OUT" | tr '\n' '|')"
    bad=$(grep -cE '^(FAIL|PASS|PROVED):' "$CHILD_OUT" || true)
    [ "${bad:-0}" -eq 0 ] \
        && pass "$desc: emitted ZERO assertion-shaped lines — nothing claims a control ran, because none did" \
        || fail "$desc: emitted $bad assertion-shaped line(s) — this is mg-3412 verbatim: findings about a tree that was never tested"
}

# ===========================================================================
# 1. POSITIVE — a working sandbox is private, and the daemon really uses it.
# ===========================================================================
# Without this every control below could be satisfied by an isolate() that
# always fails. It also proves the claim that matters operationally: the daemon
# writes its pid file, its schedule store and its event log INSIDE the sandbox.
# ~/.pogo/pogo.pid is the file the machine's live pogod owns — a sandbox daemon
# that did not have a private POGO_HOME would stamp its own pid over it.
run_child '
pogo_sandbox_create t1
trap pogo_sandbox_down EXIT
pogo_sandbox_isolate
pogo_sandbox_daemon "'"$WORK"'/pogod" /scheduler/schedules
pogo_sandbox_assert_no_schedules
pogo_sandbox_curl "could not register the namespaced probe schedule" -- \
    -X POST "$POGO_SANDBOX_URL/scheduler/schedules" -H "Content-Type: application/json" \
    -d "{\"id\":\"mail-check-$(pogo_sandbox_name pa)\",\"agent\":\"$(pogo_sandbox_name pa)\",\"cron\":\"*/10 * * * *\",\"delivery\":\"nudge\",\"message\":\"x\"}"
echo "HOME=$HOME"
echo "POGO_HOME=$POGO_HOME"
echo "XDG=$XDG_CONFIG_HOME"
echo "MG_ROOT=$MG_ROOT"
echo "ROOT=$POGO_SANDBOX_DIR"
echo "TOKEN=$POGO_SANDBOX_TOKEN"
echo "PORT=$POGO_SANDBOX_PORT"
for f in pogo.pid schedules.json events.log; do
    [ -e "$POGO_HOME/$f" ] && echo "WROTE=$f"
done
'
[ "$CHILD_RC" -eq 0 ] \
    && pass "a working sandbox comes up clean: create -> isolate -> real pogod -> a namespaced write, rc 0 (the guards below are refusals, not a harness welded shut)" \
    || fail "a working sandbox did NOT come up: rc=$CHILD_RC, output: $(head -20 "$CHILD_OUT" | tr '\n' '|')"

C_HOME="$(grep '^HOME=' "$CHILD_OUT" | head -1 | cut -d= -f2-)"
C_POGO="$(grep '^POGO_HOME=' "$CHILD_OUT" | head -1 | cut -d= -f2-)"
C_XDG="$(grep '^XDG=' "$CHILD_OUT" | head -1 | cut -d= -f2-)"
C_MG="$(grep '^MG_ROOT=' "$CHILD_OUT" | head -1 | cut -d= -f2-)"
C_ROOT="$(grep '^ROOT=' "$CHILD_OUT" | head -1 | cut -d= -f2-)"
C_TOKEN="$(grep '^TOKEN=' "$CHILD_OUT" | head -1 | cut -d= -f2-)"
C_PORT="$(grep '^PORT=' "$CHILD_OUT" | head -1 | cut -d= -f2-)"

REAL_POGO_HOME="${POGO_HOME:-$HOME/.pogo}"
LEAKED=""
for v in "$C_HOME" "$C_POGO" "$C_XDG" "$C_MG"; do
    case "$v" in
        "$C_ROOT"/*) ;;
        *) LEAKED="$LEAKED $v" ;;
    esac
    [ "$v" = "$HOME" ] && LEAKED="$LEAKED $v"
    [ "$v" = "$REAL_POGO_HOME" ] && LEAKED="$LEAKED $v"
done
[ -z "$LEAKED" ] \
    && pass "all four overrides (HOME, XDG_CONFIG_HOME, POGO_HOME, MG_ROOT) live under the private root and none is the developer's — POGO_HOME is pinned SEPARATELY because this box exports POGO_HOME=\$HOME from a stale profile, which is mg-5336's leak exactly" \
    || fail "sandbox path(s) escaped the root or matched the developer's:$LEAKED"

WROTE="$(grep -c '^WROTE=' "$CHILD_OUT" || true)"
[ "${WROTE:-0}" -eq 3 ] \
    && pass "the sandbox pogod really USED the private POGO_HOME: pogo.pid, schedules.json and events.log were written there — the isolation is observed, not merely declared (an un-isolated sandbox daemon stamps its pid over the live fleet's ~/.pogo/pogo.pid)" \
    || fail "the sandbox pogod wrote only ${WROTE:-0}/3 of pogo.pid, schedules.json, events.log into \$POGO_HOME — either it is storing state elsewhere (i.e. somewhere real) or the daemon never ran"

case "$C_TOKEN" in
    sbxt1*) pass "the run token ('$C_TOKEN') is a string no live fleet holds, so a name built from it cannot collide with a real agent or schedule" ;;
    *)      fail "the run token is '$C_TOKEN' — it must carry the sbx prefix that makes it unmistakable" ;;
esac

[ -n "$C_PORT" ] && [ "$C_PORT" -ge 18500 ] && [ "$C_PORT" -le 18999 ] \
    && pass "the daemon took a reserved port ($C_PORT) from the private range, not the fixed 17731 mg-3412 drew while the real one was live" \
    || fail "the sandbox daemon port was '$C_PORT', outside the reserved range"

[ ! -d "$C_ROOT" ] \
    && pass "pogo_sandbox_down removed the root on the way out — a suite cannot leak a sandbox into /var/folders" \
    || fail "the sandbox root $C_ROOT survived teardown"

# ===========================================================================
# 2. THE HOME LEAK — a sandbox whose HOME resolves onto the developer's.
# ===========================================================================
# The shape mg-6092/mg-e8e7/mg-5336 all took, staged: every string says private
# and one inode is not. Only a check that RESOLVES symlinks can see it, which is
# why pogo_sandbox_isolate resolves rather than compares.
mkdir -p "$WORK/leak"
ln -s "$HOME" "$WORK/leak/home"
run_child '
pogo_sandbox_create t2
trap pogo_sandbox_down EXIT
pogo_sandbox_isolate
echo "PASS: reached the controls with a leaking HOME"
' POGO_SANDBOX_ROOT="$WORK/leak"
assert_setup_failure "a sandbox HOME that is a symlink to the developer's home"
grep -q "is the developer's" "$CHILD_OUT" \
    && pass "...and the message NAMES the live path it resolved onto, so the reader is told what is wrong rather than that something is" \
    || fail "the leak refusal did not name the developer's path: $(head -8 "$CHILD_OUT" | tr '\n' '|')"
[ -d "$HOME" ] && [ -n "$(ls -A "$HOME" 2>/dev/null)" ] \
    && pass "...and the developer's home is untouched — the refusal came BEFORE the teardown that rm -rf's the root" \
    || fail "the developer's home was damaged by a rejected sandbox — this is the direction that cannot be undone"

# The same leak against an EMPTY decoy home, and the reason it is worth a second
# case. isolate has a SECOND, cruder guard — "a freshly created POGO_HOME is
# empty" — and against the real ~/.pogo that one fires too, so the case above
# comes back 99 even with the symlink resolution removed. That makes its exit
# code uninformative about the guarantee under test; only its message
# discriminates. Pointing the link at an empty home takes the crude guard out of
# play, so nothing but the resolve-through-symlinks check stands between this
# child and a HOME it does not own.
mkdir -p "$WORK/decoy-home2/.pogo"
mkdir -p "$WORK/leak-empty"
ln -s "$WORK/decoy-home2" "$WORK/leak-empty/home"
run_child '
pogo_sandbox_create t2b
trap pogo_sandbox_down EXIT
pogo_sandbox_isolate
echo "PASS: reached the controls with a leaking HOME"
' HOME="$WORK/decoy-home2" POGO_HOME="$WORK/decoy-home2/.pogo" POGO_SANDBOX_ROOT="$WORK/leak-empty"
assert_setup_failure "a sandbox HOME symlinked to an EMPTY home (only the symlink resolution can catch it)"

# ===========================================================================
# 3. THE rm -rf GUARD — a root that IS a home is refused, and survives.
# ===========================================================================
# Teardown removes the root, so "the sandbox root is the developer's home" is not
# a read-only mistake: it is `rm -rf $HOME`. The check therefore runs in
# pogo_sandbox_create, before the path is stored anywhere teardown could find it.
#
# Driven against a DECOY home rather than the real one — HOME is overridden for
# the child, so the harness's notion of "the developer's home" is the decoy. The
# guard is exercised for real; the machine is not the thing being bet.
mkdir -p "$WORK/decoy-home/.pogo"
echo "precious" > "$WORK/decoy-home/.pogo/live-state"
run_child '
pogo_sandbox_create t3
trap pogo_sandbox_down EXIT
pogo_sandbox_isolate
echo "PASS: reached the controls with the home as the root"
' HOME="$WORK/decoy-home" POGO_HOME="$WORK/decoy-home/.pogo" POGO_SANDBOX_ROOT="$WORK/decoy-home"
assert_setup_failure "a sandbox root that IS the home directory"
[ -f "$WORK/decoy-home/.pogo/live-state" ] \
    && pass "...and the home it was pointed at still has its contents: the root is vetted in create(), so no EXIT trap ever gets a live path to remove" \
    || fail "THE UNRECOVERABLE ONE: a rejected sandbox root was still torn down, and it took the home's live state with it"

# ===========================================================================
# 4. THE NAME GUARD — mail-check-pa cannot be written, in any of its forms.
# ===========================================================================
# mg-3412's suite removed and restored a schedule literally named
# `mail-check-pa`, which is a real crew loop. Namespacing was then added as a
# CONVENTION; this is the version that does not rely on anybody keeping it.
#
# EVERY CASE RUNS AGAINST A REAL, LIVE SANDBOX DAEMON THAT WOULD ACCEPT THE
# WRITE, and the DELETE case pre-stages `mail-check-pa` through a RAW curl so the
# removal it attempts is one that would really succeed. That is deliberate and it
# was learned the hard way: an earlier draft pointed these at a dead port, and
# every case "passed" — not because the name guard refused them but because the
# staging curl failed and raised the same exit code. It was a control that could
# not return anything but green, which is the exact shape mg-c02d catalogued
# (an awk filter that matched every line). With a daemon that would say yes, the
# only thing standing between the write and the fleet's namespace is the guard,
# so removing the guard turns these red.
for case_desc in body-id body-agent url-path; do
    case "$case_desc" in
        body-id)
            prestage=""
            snippet='pogo_sandbox_curl "register" -- -X POST "$POGO_SANDBOX_URL/scheduler/schedules" -H "Content-Type: application/json" -d "{\"id\":\"mail-check-pa\",\"agent\":\"pa\",\"cron\":\"*/10 * * * *\",\"delivery\":\"nudge\",\"message\":\"x\"}"'
            label="a schedule id naming the live fleet's mail-check-pa"
            offender="mail-check-pa" ;;
        body-agent)
            prestage=""
            snippet='pogo_sandbox_curl "register" -- -X POST "$POGO_SANDBOX_URL/scheduler/schedules" -H "Content-Type: application/json" -d "{\"id\":\"mail-check-$(pogo_sandbox_name pa)\",\"agent\":\"pa\",\"cron\":\"*/10 * * * *\",\"delivery\":\"nudge\",\"message\":\"x\"}"'
            label="an agent field naming the live crew agent pa"
            offender="pa" ;;
        url-path)
            # Raw curl, NOT pogo_sandbox_curl: this stages the state the guarded
            # DELETE below would really remove, so the removal is a live one.
            prestage='curl -sf --max-time 10 -X POST "$POGO_SANDBOX_URL/scheduler/schedules" -H "Content-Type: application/json" -d "{\"id\":\"mail-check-pa\",\"agent\":\"pa\",\"cron\":\"*/10 * * * *\",\"delivery\":\"nudge\",\"message\":\"x\"}" >/dev/null || { echo "PRESTAGE FAILED"; exit 3; }'
            snippet='pogo_sandbox_curl "remove" -- -X DELETE "$POGO_SANDBOX_URL/scheduler/schedules/mail-check-pa"'
            label="a DELETE whose URL names mail-check-pa"
            offender="mail-check-pa" ;;
    esac
    run_child "
pogo_sandbox_create t4
trap pogo_sandbox_down EXIT
pogo_sandbox_isolate
pogo_sandbox_daemon \"$WORK/pogod\" /scheduler/schedules
$prestage
$snippet
echo \"PASS: the write went through — it reached a daemon that accepted it\"
"
    assert_setup_failure "$label"
    grep -q "not namespaced to this run" "$CHILD_OUT" \
        && grep -q "'$offender'" "$CHILD_OUT" \
        && pass "$label: the refusal is the NAME guard's — it says the identity is not namespaced, names '$offender', and points at pogo_sandbox_name, rather than being some other failure with the same exit code" \
        || fail "$label: exit $SETUP_RC was raised by something other than the name guard (no 'not namespaced to this run' naming '$offender'): $(head -8 "$CHILD_OUT" | tr '\n' '|')"
done

# The other direction: the guard must not simply refuse everything. §1 already
# registered a namespaced mail-check through pogo_sandbox_curl and came back rc 0
# — the `mail-check-` PREFIX survives, which it must, because
# extract_mail_check_ids keys on it and it is part of the seam under test.
pass "NEGATIVE: a namespaced mail-check IS accepted (§1 registered one end to end) — the guard discriminates on the identity, not on the prefix the driver parses"

# ===========================================================================
# 5. ORDERING — a daemon may not be started before the home is private.
# ===========================================================================
# A pogod launched under the developer's HOME writes ~/.pogo/pogo.pid, loads the
# real crew prompts and can auto-start them, whatever port it bound. The port was
# never the whole of the isolation, and a harness that let you take them in the
# wrong order would be back to remembering.
#
# Driven against a DECOY home, like §3: if this guard were ever removed the child
# would really boot a pogod under whatever $HOME it was handed, and that daemon
# really would stamp its pid over ~/.pogo/pogo.pid. A control that damages the
# machine when it fails is not one anybody will keep running.
mkdir -p "$WORK/decoy-home5/.pogo"
run_child '
pogo_sandbox_create t5
trap pogo_sandbox_down EXIT
pogo_sandbox_daemon "'"$WORK"'/pogod" /scheduler/schedules
echo "PASS: a daemon booted under the developer HOME"
' HOME="$WORK/decoy-home5" POGO_HOME="$WORK/decoy-home5/.pogo"
assert_setup_failure "a daemon started before pogo_sandbox_isolate"
grep -q "before pogo_sandbox_isolate" "$CHILD_OUT" \
    && pass "...and the refusal is the ORDERING guard's, not some other failure that happens to share the exit code" \
    || fail "exit $SETUP_RC was raised by something other than the ordering guard: $(head -8 "$CHILD_OUT" | tr '\n' '|')"

# ===========================================================================
# 6. END TO END — the CONVERTED control, with the sandbox broken.
# ===========================================================================
# scripts/pogo-self-deploy_live_test.sh is the suite mg-3412 was filed against
# and the one that gates every merge. This is mg-78a5's acceptance in one
# assertion: point it at a sandbox that cannot be private and it must report a
# SETUP failure — not the fourteen assertion failures that cost a coordinator
# fifteen minutes and a merge.
#
# The binaries are handed in prebuilt so the run reaches the isolation step in
# seconds rather than rebuilding the tree to be refused.
echo "Building the pogo CLI for the end-to-end control..."
if ! (cd "$REPO_ROOT" && go build -o "$WORK/pogo" ./cmd/pogo); then
    echo "could not build cmd/pogo — the end-to-end control cannot run"; exit 1
fi
mkdir -p "$WORK/leak2"
ln -s "$HOME" "$WORK/leak2/home"
E2E_OUT="$WORK/e2e.out"
E2E_RC=0
env POGO_SANDBOX_ROOT="$WORK/leak2" \
    POGO_LIVE_CONTROL_POGOD="$WORK/pogod" \
    POGO_LIVE_CONTROL_POGO="$WORK/pogo" \
    bash "$REPO_ROOT/scripts/pogo-self-deploy_live_test.sh" > "$E2E_OUT" 2>&1 || E2E_RC=$?

[ "$E2E_RC" = "$SETUP_RC" ] \
    && pass "THE ASK: the converted live control, pointed at a sandbox that would reach the developer's home, exits $SETUP_RC — a SETUP failure, distinguishable by exit code alone from the assertion tally's 1" \
    || fail "the converted live control exited $E2E_RC against a broken sandbox, expected $SETUP_RC: $(head -10 "$E2E_OUT" | tr '\n' '|')"

E2E_BAD=$(grep -cE '^(FAIL|PASS|PROVED):' "$E2E_OUT" || true)
[ "${E2E_BAD:-0}" -eq 0 ] \
    && pass "THE ASK, the other half: it emitted ZERO assertion-shaped lines — no FAIL: to be read as a regression, no PASS: banking credibility it never earned, and no PROVED: token for do_prove to deploy on" \
    || fail "the converted live control emitted ${E2E_BAD} assertion-shaped line(s) downstream of a setup failure — mg-3412's cascade, reproduced: $(grep -E '^(FAIL|PASS|PROVED):' "$E2E_OUT" | head -3 | tr '\n' '|')"

grep -q "SETUP FAILURE" "$E2E_OUT" \
    && pass "...and it says so in words: the reader of a failed 03:00 gate is told the run makes no claim about the tree, instead of being handed findings" \
    || fail "the converted live control printed no SETUP FAILURE banner: $(head -10 "$E2E_OUT" | tr '\n' '|')"

# ===========================================================================
# 7. THE TOOLCHAIN PIN — a private HOME must not cost a toolchain download.
# ===========================================================================
# mg-cdf1: the private HOME empties Go's module cache, so with GOTOOLCHAIN=auto
# and `go 1.25.0` in go.mod the next `go` call — `go env GOBIN`, no build needed —
# fetches ~70 MB of toolchain into a directory teardown deletes. Measured at
# ~32 KB/s: ~36 minutes, inside ./test.sh, which is the refinery gate. It cost
# three consecutive 60-minute gate timeouts on one merge, each reported as a
# verdict on the branch.
#
# Both directions in one child, because the positive alone would prove nothing
# here: the bug only exists with a COLD cache, and a sandbox HOME created seconds
# ago is cold by construction — so the unpinned half below is a live reproduction
# of the defect in the same HOME, not a simulation of it. If Go ever stops
# needing the fetch, the unpinned half goes quiet and says so.
#
# GOPROXY=off throughout: it turns "downloads 70 MB" into "says it would have"
# in milliseconds. Without it this control would BE the 36-minute stall.
#
# GOMODCACHE is put BACK to the HOME-derived path on the unpinned half, and that
# line is load-bearing (mg-a9d8). isolate now pins the module cache at the real
# one as well, and the toolchain lives IN that cache — so an "unpinned" half that
# inherited the pin would resolve go1.25.0 offline, print no download line, and
# this control would go quietly vacuous while still passing. It did, for one run,
# during mg-a9d8: the remedy for one download silently disabled the proof that
# the other one happens.
run_child '
pogo_sandbox_create t7
trap pogo_sandbox_down EXIT
PRE_PATH="$PATH"
pogo_sandbox_isolate
cd "'"$REPO_ROOT"'" || exit 1
echo "REALGO=$POGO_SANDBOX_REAL_GOVERSION"
echo "TOOLCHAIN=${GOTOOLCHAIN:-<unset>}"
echo "---PINNED---"
GOPROXY=off go env GOVERSION 2>&1
echo "---UNPINNED---"
env GOPROXY=off GOTOOLCHAIN=auto GOMODCACHE="$HOME/go/pkg/mod" PATH="$PRE_PATH" go env GOVERSION 2>&1
echo "---END---"
'
[ "$CHILD_RC" -eq 0 ] \
    && pass "a sandbox that pins its toolchain still isolates cleanly (rc 0) — the pin is checked inside isolate, so a pin that failed to take would have ended the run here" \
    || fail "the toolchain-pin child exited $CHILD_RC: $(head -20 "$CHILD_OUT" | tr '\n' '|')"

TC_REALGO="$(grep '^REALGO=' "$CHILD_OUT" | head -1 | cut -d= -f2-)"
TC_PINNED="$(sed -n '/^---PINNED---$/,/^---UNPINNED---$/p' "$CHILD_OUT")"
TC_UNPINNED="$(sed -n '/^---UNPINNED---$/,/^---END---$/p' "$CHILD_OUT")"

grep -q '^TOOLCHAIN=local$' "$CHILD_OUT" \
    && pass "isolate exports GOTOOLCHAIN=local — with the resolved GOROOT ahead of it on PATH, so 'local' names the toolchain go.mod actually requires and not the older one installed at /opt/homebrew" \
    || fail "isolate did not export GOTOOLCHAIN=local: $(grep '^TOOLCHAIN=' "$CHILD_OUT" | head -1)"

# The claim that matters operationally: under the sandbox HOME, with the network
# refused, `go` still answers — because it never wanted the network.
if [ -n "$TC_REALGO" ] && printf '%s' "$TC_PINNED" | grep -qF "$TC_REALGO"; then
    pass "with the pin, a COLD sandbox HOME resolves $TC_REALGO with GOPROXY=off — \`go\` answers without reaching the network at all, which is the whole 36 minutes"
else
    fail "with the pin, a cold sandbox HOME did not resolve ${TC_REALGO:-the real toolchain}: $(printf '%s' "$TC_PINNED" | tr '\n' '|')"
fi

printf '%s' "$TC_PINNED" | grep -qi 'downloading' \
    && fail "the pinned half still tried to download a toolchain — the pin did not take: $(printf '%s' "$TC_PINNED" | tr '\n' '|')" \
    || pass "...and it printed no 'downloading' line: nothing is fetched into a HOME that is removed at teardown"

# The positive control on the control. Same cold HOME, pin removed.
printf '%s' "$TC_UNPINNED" | grep -qi 'downloading go1\.' \
    && pass "PROVED RED: the same cold HOME WITHOUT the pin does try to download a toolchain — this control is exercising the live defect, not asserting a tautology" \
    || fail "the unpinned half did NOT attempt a toolchain download, so the pinned half proves nothing: $(printf '%s' "$TC_UNPINNED" | tr '\n' '|')"

# The way the CURE reintroduces the DISEASE, which it did on the first draft.
# The capture asks `go env` which toolchain is already resolved, and `go env`
# obeys the same toolchain rule as everything else — so run under an AMBIENT home
# that is itself cold (§4 and §5 below do exactly that, with decoy HOMEs), the
# capture starts the very 36-minute fetch it exists to prevent, before the
# sandbox HOME is even in play. GOPROXY=off on the capture is what stops it, and
# a cold ambient HOME must simply yield no pin.
COLD_AMBIENT="$WORK/cold-ambient-home"
mkdir -p "$COLD_AMBIENT"
run_child '
pogo_sandbox_create t7b
trap pogo_sandbox_down EXIT
pogo_sandbox_isolate
echo "TOOLCHAIN=${GOTOOLCHAIN:-<unset>}"
echo "REALGO=${POGO_SANDBOX_REAL_GOVERSION:-<none>}"
' HOME="$COLD_AMBIENT" POGO_HOME="$COLD_AMBIENT/.pogo"

[ "$CHILD_RC" -eq 0 ] && grep -q '^TOOLCHAIN=<unset>$' "$CHILD_OUT" \
    && pass "an ambient HOME with no toolchain of its own yields NO pin (GOTOOLCHAIN left unset) rather than fetching one — nothing to share is not the same as something to download" \
    || fail "a cold ambient HOME did not decline the pin: rc=$CHILD_RC, $(grep -E '^(TOOLCHAIN|REALGO)=' "$CHILD_OUT" | tr '\n' '|')"

# Asserted on the ZIP, not on the directory. `go env` creates
# cache/download/golang.org/toolchain/@v/ and takes the .lock BEFORE it discovers
# the network is refused, so the tree existing proves nothing — it exists either
# way. The `.zip`/`.zip*.tmp` is the artifact of the transfer itself: it is fd 5
# in the ticket's lsof, 30.8 MB and growing, and it is absent when nothing is
# fetched.
TC_ZIPS=$(find "$COLD_AMBIENT/go/pkg/mod" -name 'v0.0.1-go*.zip*' 2>/dev/null | wc -l | tr -d ' ')
[ "${TC_ZIPS:-0}" -eq 0 ] \
    && pass "...and it transferred nothing: zero toolchain .zip/.zip.tmp under that HOME (the .lock is taken either way — the zip is the 70 MB)" \
    || fail "the harness started a toolchain download into the ambient HOME ($COLD_AMBIENT): $TC_ZIPS zip artifact(s) — the capture is the disease again, one layer earlier"

# ===========================================================================
# 8. THE MODULE PIN — a private HOME must not cost the module graph either.
# ===========================================================================
# mg-cdf1 closed the TOOLCHAIN half of the download the HOME move causes. The
# MODULE half stayed open for five days: the same empty cache means the next build
# must fetch, from proxy.golang.org, every module the packages under test import.
# Measured on 2026-08-19 against a file:// proxy: one run of
# scripts/tmpdir-leak_test.sh fetches 2 zips and 2 .mod files, 752 KB, ONCE — the
# six `go test` invocations share one sandbox HOME, so only the second fetches.
# (That withdraws the ticket's unmeasured "six times per run".) Every byte of it
# is ./internal/agent: the five-package slice imports no external module at all,
# which is why this failure mode can only ever name that one package. On
# 2026-08-14T03:46Z the resolver blinked mid-fetch and it landed as
#
#     internal/agent/terminal.go:9:2: nhooyr.io/websocket@v1.8.17: ...
#       dial tcp: lookup proxy.golang.org: no such host
#     FAIL  github.com/drellem2/pogo/internal/agent [setup failed]
#
# in a package that PASSED in the same gate run, was classed DEFECT, and was not
# retried (mg-a9d8). Both directions in one child again, and for the same reason
# §7 needs both: the sandbox HOME is cold by construction, so the unpinned half
# is the live defect rather than a simulation of it.
#
# GOPROXY=off on the unpinned half turns "downloads the module graph" into "says
# it would have" — which is the same trick §7 uses, and the reason this control
# costs seconds rather than minutes of somebody else's bandwidth.
run_child '
pogo_sandbox_create t8
trap pogo_sandbox_down EXIT
pogo_sandbox_isolate
cd "'"$REPO_ROOT"'" || exit 1
echo "PINNED=${POGO_SANDBOX_MODCACHE_PINNED:-<none>}"
echo "REALMC=${POGO_SANDBOX_REAL_GOMODCACHE:-<none>}"
echo "MODCACHE=$(go env GOMODCACHE)"
echo "PROXY=$(go env GOPROXY)"
echo "HOMEMC=$HOME/go/pkg/mod"
echo "---BUILD---"
go build -o /dev/null ./internal/agent 2>&1
echo "BUILDRC=$?"
# Read BEFORE the unpinned half below, which points GOMODCACHE at this very
# directory and so creates cache/ in it on its way to failing.
echo "SANDBOX_MC_ENTRIES=$(find "$HOME/go/pkg/mod" -mindepth 1 -maxdepth 1 2>/dev/null | wc -l | tr -d " ")"
echo "---UNPINNED---"
env GOMODCACHE="$HOME/go/pkg/mod" GOPROXY=off go build -o /dev/null ./internal/agent 2>&1
echo "---END---"
'
[ "$CHILD_RC" -eq 0 ] \
    && pass "a sandbox that pins its module cache still isolates cleanly (rc 0) — the pin is proven inside isolate, so a pin that failed to take would have ended the run here" \
    || fail "the module-pin child exited $CHILD_RC: $(head -20 "$CHILD_OUT" | tr '\n' '|')"

MC_REAL="$(grep '^REALMC=' "$CHILD_OUT" | head -1 | cut -d= -f2-)"
MC_SEEN="$(grep '^MODCACHE=' "$CHILD_OUT" | head -1 | cut -d= -f2-)"
MC_HOME="$(grep '^HOMEMC=' "$CHILD_OUT" | head -1 | cut -d= -f2-)"
MC_BUILD="$(sed -n '/^---BUILD---$/,/^---UNPINNED---$/p' "$CHILD_OUT")"
MC_UNPINNED="$(sed -n '/^---UNPINNED---$/,/^---END---$/p' "$CHILD_OUT")"

if [ -n "$MC_REAL" ] && [ "$MC_REAL" != "<none>" ] && [ "$MC_SEEN" = "$MC_REAL" ]; then
    pass "under the private HOME, \`go env GOMODCACHE\` resolves the REAL cache ($MC_SEEN) and not the HOME-derived one ($MC_HOME) — the module graph is already there, so there is nothing to fetch"
else
    fail "the module cache was not pinned: go env said '$MC_SEEN', the real environment says '${MC_REAL:-<none>}' (HOME-derived would be $MC_HOME)"
fi

grep -q '^PROXY=off$' "$CHILD_OUT" \
    && pass "...and GOPROXY=off with it, which is what makes the pin SAFE rather than merely fast: with no download path there is no write into the developer's cache, which is the hazard the pin was refused over until it was measured (mg-a9d8)" \
    || fail "isolate pinned GOMODCACHE without closing the download path: $(grep '^PROXY=' "$CHILD_OUT" | head -1) — a fetch inside this run would WRITE into the developer's cache"

# The claim that matters operationally, and the one the 2026-08-14 failure is the
# absence of: a full build of the package that failed that night, under the
# sandbox HOME, with the network refused.
if grep -q '^BUILDRC=0$' "$CHILD_OUT" && ! printf '%s' "$MC_BUILD" | grep -qi 'downloading\|proxy.golang.org'; then
    pass "with the pin, a COLD sandbox HOME builds ./internal/agent — the package the 2026-08-14 gate failure named — with no download and no network at all"
else
    fail "with the pin, ./internal/agent did not build offline under the sandbox HOME: $(printf '%s' "$MC_BUILD" | tr '\n' '|' | cut -c1-400)"
fi

# The positive control on the control. Same cold HOME, same build, cache left
# where the HOME move puts it.
printf '%s' "$MC_UNPINNED" | grep -qE 'module lookup disabled|downloading' \
    && pass "PROVED RED: the same cold HOME with the cache left HOME-derived tries to fetch the module graph — this control is exercising the live defect, not asserting a tautology" \
    || fail "the unpinned half did NOT try to fetch a module, so the pinned half proves nothing: $(printf '%s' "$MC_UNPINNED" | tr '\n' '|' | cut -c1-400)"

# Nothing was written into the sandbox's own cache either — which is the
# directory teardown deletes, and where the six-per-run download used to land.
MC_SANDBOX_ENTRIES="$(grep '^SANDBOX_MC_ENTRIES=' "$CHILD_OUT" | head -1 | cut -d= -f2-)"
[ "${MC_SANDBOX_ENTRIES:-x}" = "0" ] \
    && pass "...and the sandbox's own HOME-derived cache stayed EMPTY across the build: nothing was fetched into a directory this run removes" \
    || fail "the sandbox HOME-derived module cache holds ${MC_SANDBOX_ENTRIES:-<unread>} entries after a pinned build — something is still downloading into a throwaway root"

# A cold ambient HOME declines the module pin for the same reason it declines the
# toolchain one: the capture asks what this environment ALREADY has, and "none"
# is an answer. Reusing §7's decoy HOME, which has no toolchain either.
run_child '
pogo_sandbox_create t8b
trap pogo_sandbox_down EXIT
pogo_sandbox_isolate
echo "PINNED=${POGO_SANDBOX_MODCACHE_PINNED:-<none>}"
echo "PROXY=${GOPROXY:-<unset>}"
' HOME="$COLD_AMBIENT" POGO_HOME="$COLD_AMBIENT/.pogo"

[ "$CHILD_RC" -eq 0 ] && grep -q '^PINNED=<none>$' "$CHILD_OUT" && grep -q '^PROXY=<unset>$' "$CHILD_OUT" \
    && pass "an ambient HOME with no cache of its own yields NO module pin and leaves GOPROXY alone — failing OPEN, because an unpinned cache is the behaviour we already had and a wrongly-closed proxy would be a new hard failure" \
    || fail "a cold ambient HOME did not decline the module pin: rc=$CHILD_RC, $(grep -E '^(PINNED|PROXY)=' "$CHILD_OUT" | tr '\n' '|')"

# THE PROOF ITSELF, exercised. Everything above shows the pin TAKING; a pin that
# is only ever seen working is a pin whose failure path has never run, and the
# failure path is the whole reason the pin is written this way — mg-cdf1's
# toolchain pin is proven under the private HOME precisely because an unproven
# pin fails an hour downstream wearing the branch's name.
#
# Driven by putting a `go` on PATH that ignores its environment, which is exactly
# what a pin that did not take looks like from the inside, and calling the pin
# directly so the shim is not shadowed by the GOROOT the toolchain pin prepends.
# Both halves get their own child, because either alone is a live hazard: the
# cache without the proxy closed writes into the developer's, and the proxy
# closed without the cache turns every sandboxed build into a hard failure.
run_child '
pogo_sandbox_create t8c
trap pogo_sandbox_down EXIT
mkdir -p "$POGO_SANDBOX_DIR/decoybin" "'"$WORK"'/fakerealcache"
cat > "$POGO_SANDBOX_DIR/decoybin/go" <<"SHIM"
#!/usr/bin/env bash
echo /some/other/cache
echo off
SHIM
chmod +x "$POGO_SANDBOX_DIR/decoybin/go"
PATH="$POGO_SANDBOX_DIR/decoybin:$PATH"
POGO_SANDBOX_REAL_GOMODCACHE="'"$WORK"'/fakerealcache"
pogo_sandbox__pin_modcache
echo "REACHED THE LINE AFTER THE PIN"
'
assert_setup_failure "a module-cache pin that did not take"
grep -q 'module-cache pin did not take' "$CHILD_OUT" && ! grep -q 'REACHED THE LINE AFTER THE PIN' "$CHILD_OUT" \
    && pass "...and the refusal is the MODULE PIN's, and it ENDS the run — the proof is load-bearing, not decoration, so a sandbox cannot proceed into a build that would reach the network" \
    || fail "a module-cache pin that did not take was not refused by the pin: $(head -6 "$CHILD_OUT" | tr '\n' '|')"

run_child '
pogo_sandbox_create t8d
trap pogo_sandbox_down EXIT
mkdir -p "$POGO_SANDBOX_DIR/decoybin" "'"$WORK"'/fakerealcache2"
cat > "$POGO_SANDBOX_DIR/decoybin/go" <<"SHIM"
#!/usr/bin/env bash
echo "'"$WORK"'/fakerealcache2"
echo https://proxy.golang.org,direct
SHIM
chmod +x "$POGO_SANDBOX_DIR/decoybin/go"
PATH="$POGO_SANDBOX_DIR/decoybin:$PATH"
POGO_SANDBOX_REAL_GOMODCACHE="'"$WORK"'/fakerealcache2"
pogo_sandbox__pin_modcache
echo "REACHED THE LINE AFTER THE PIN"
'
assert_setup_failure "a pinned cache whose download path is still open"
grep -q "not 'off'" "$CHILD_OUT" && ! grep -q 'REACHED THE LINE AFTER THE PIN' "$CHILD_OUT" \
    && pass "...and the refusal names the OPEN DOWNLOAD PATH specifically — the cache pinned and the proxy live is the one combination that writes into the developer's cache, and it is refused rather than run" \
    || fail "a pinned cache with a live proxy was not refused: $(head -6 "$CHILD_OUT" | tr '\n' '|')"

# ===========================================================================
# 9. THE TRANSLATION — a module failure must not be reported as somebody else's.
# ===========================================================================
# The half of mg-a9d8 that is worth having even if the pin is someday removed.
# The 2026-08-14 log really did contain the proxy line; what a person read at the
# top of the report was "SETUP: the fixture-creating suite did not pass on the
# third run", so the first place three agents looked was internal/agent and
# $TMPDIR. Both directions, because a classifier that fires on everything is the
# same defect with the arrow reversed — it would relabel a genuine assertion
# failure as weather and send the next reader away from a real bug.
MODLOG="$WORK/modfail.log"
cat > "$MODLOG" <<'MODEOF'
ok  	github.com/drellem2/pogo/internal/events	0.395s
internal/agent/terminal.go:9:2: nhooyr.io/websocket@v1.8.17: Get "https://proxy.golang.org/nhooyr.io/websocket/@v/v1.8.17.zip": dial tcp: lookup proxy.golang.org: no such host
FAIL	github.com/drellem2/pogo/internal/agent [setup failed]
MODEOF
MOD_OUT="$WORK/modfail.out"
( source "$REPO_ROOT/scripts/pogo-sandbox"; pogo_sandbox_go_module_failure "$MODLOG" ) > "$MOD_OUT" 2>&1
MOD_RC=$?
if [ "$MOD_RC" -eq 0 ] && grep -q 'NOT A DEFECT IN THE PACKAGE NAMED ABOVE' "$MOD_OUT" && grep -qi 'NETWORK failure' "$MOD_OUT"; then
    pass "the 2026-08-14 log verbatim is reported as a NETWORK failure fetching modules, not as a failure of internal/agent — which passed in 358s in the same gate run"
else
    fail "the 2026-08-14 log was not translated (rc=$MOD_RC): $(head -4 "$MOD_OUT" | tr '\n' '|')"
fi

CFGLOG="$WORK/cfgfail.log"
cat > "$CFGLOG" <<'CFGEOF'
go: downloading nhooyr.io/websocket v1.8.17
internal/agent/terminal.go:9:2: module lookup disabled by GOPROXY=off
FAIL	github.com/drellem2/pogo/internal/agent [setup failed]
CFGEOF
( source "$REPO_ROOT/scripts/pogo-sandbox"; pogo_sandbox_go_module_failure "$CFGLOG" ) > "$MOD_OUT" 2>&1
MOD_RC=$?
if [ "$MOD_RC" -eq 0 ] && grep -q 'MODULE CACHE MISS' "$MOD_OUT" && grep -q 'go mod download' "$MOD_OUT"; then
    pass "the pin's OWN failure mode — a module the pinned cache does not hold — is reported as a cache miss with the one command that fixes it, and explicitly as something re-running will not change"
else
    fail "the GOPROXY=off cache-miss log was not translated (rc=$MOD_RC): $(head -4 "$MOD_OUT" | tr '\n' '|')"
fi

REALLOG="$WORK/realfail.log"
cat > "$REALLOG" <<'REALEOF'
--- FAIL: TestWitnessStoreRoot (0.01s)
    witness_test.go:44: got "/tmp/x", want "/tmp/y"
FAIL	github.com/drellem2/pogo/internal/agent	0.312s
REALEOF
( source "$REPO_ROOT/scripts/pogo-sandbox"; pogo_sandbox_go_module_failure "$REALLOG" ) > "$MOD_OUT" 2>&1
MOD_RC=$?
if [ "$MOD_RC" -ne 0 ] && [ ! -s "$MOD_OUT" ]; then
    pass "PROVED THE OTHER WAY: a genuine assertion failure is NOT translated (rc=$MOD_RC, no output) — the caller falls through to printing the log, and a real bug is not relabelled as weather"
else
    fail "an ordinary assertion failure was reported as a module failure (rc=$MOD_RC): $(head -4 "$MOD_OUT" | tr '\n' '|')"
fi

echo ""
[ -s "$RESULTS" ] || { echo "ledger unreadable/empty — verdict cannot be trusted"; exit 1; }
PASS_COUNT=$(grep -c '^PASS:' "$RESULTS" 2>/dev/null || true)
FAIL_COUNT=$(grep -c '^FAIL:' "$RESULTS" 2>/dev/null || true)
PASS_COUNT=${PASS_COUNT:-0}; FAIL_COUNT=${FAIL_COUNT:-0}
echo "=== Results: $PASS_COUNT passed, $FAIL_COUNT failed ==="
if [ "$FAIL_COUNT" -gt 0 ]; then
    echo ""; echo "Failures:"; grep '^FAIL:' "$RESULTS" | sed 's/^/  /'
    exit 1
fi
