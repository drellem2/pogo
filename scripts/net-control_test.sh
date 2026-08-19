#!/usr/bin/env bash
# Tests for scripts/lib/net-control.sh — the runner-side network positive
# control (mg-db96).
#
# ---------------------------------------------------------------------------
# THE ONE ASSERTION THIS SUITE EXISTS FOR
# ---------------------------------------------------------------------------
# A positive control that has never been observed failing is not known to work.
# Every other test in here is scaffolding around section 2, which puts the
# control on a box that GENUINELY HAS NO NETWORK and requires it to report RED.
#
# "Genuinely" is doing real work in that sentence, so here is exactly what is
# arranged and why it is not a mock:
#
#   Darwin: sandbox-exec with `(deny network-outbound)` plus an allow for
#           localhost. Off-box connect(2) is refused BY THE KERNEL with EPERM;
#           loopback still routes. That is not a stub of a downed NIC, it is the
#           same observable state — a box with en0 down still reaches 127.0.0.1.
#   Linux:  `unshare -rn`, a fresh network namespace with nothing in it but lo.
#           Same shape, same reason.
#
# Nothing in the control is stubbed for that section. The real script, the real
# nc, real sockets, real syscalls — and the answer must be `down`.
#
# If NEITHER isolation mechanism is available, this suite FAILS rather than
# skipping. That is deliberate and it is the ticket's own bar: an unproven
# positive control is worse than none, because a green from it will be cited as
# evidence by someone who assumes the red was ever demonstrated. A skip here
# would be that assumption, written down.
#
# Section 3 adds the second real shape — SYNs into a real blackhole, which is
# how this host's DHCP outages present (mg-964e) — because EPERM fails FAST and
# a blackhole fails SLOW, and a control could plausibly handle one and not the
# other.
#
# Section 2b is what makes section 3 mean anything, and it is here because it
# was missing (mg-12aa). Sections 3, 4e and 5 used to NAME three RFC 5737
# documentation addresses and take the naming for the arranging. On this host,
# with a VPN holding the default route, those addresses complete a TCP handshake
# in 0.09s — so the three assertions reported a substrate that was not a
# blackhole, in the CONTROL's voice, and took `./build.sh` red on pristine main
# for six days. The substrate is now measured before it is used, with the
# control's own probe, and it must be a blackhole in both senses of the word:
# nothing answers, AND nothing answers slowly. A refusal satisfies the first and
# not the second, and only the second is the shape section 3 exists for.
#
# Sections 4 onward are the other half of the bar: the control must be
# distinguishable from a control that is merely broken. Each one drives it into
# a state where it cannot measure and requires `unknown`, never `down`.
set -u

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
LIB="$HERE/lib/net-control.sh"

# The isolation envelope, and the case for it here is not that this suite is
# known to reach the developer's live ~/.pogo — it is that "this one only runs
# nc against loopback" is exactly the reasoning that put 48 suites in the
# adoption ledger. The suite also runs the control under sandbox-exec and
# unshare, where an unexpected read of live state would fail in a way that looks
# like a network verdict.
# shellcheck source=/dev/null
source "$HERE/pogo-sandbox"
pogo_sandbox_create netcontrol
# BH_SINK_PID is a background python process this suite may start (section 2b).
# The signal list is not decoration: with a bare `trap ... EXIT` the handler does
# not run on SIGTERM, and a SYN sink that outlives the suite holds a loopback
# port with a permanently full accept queue.
BH_SINK_PID=""
netc_teardown() { [ -n "$BH_SINK_PID" ] && kill "$BH_SINK_PID" 2>/dev/null; pogo_sandbox_down; }
trap netc_teardown EXIT INT TERM HUP
pogo_sandbox_isolate

PASS=0
FAIL=0
pass() { PASS=$((PASS + 1)); echo "  PASS: $1"; }
fail() { FAIL=$((FAIL + 1)); echo "  FAIL: $1" >&2; }

WORK="$(mktemp -d)"

echo "=== net-control.sh tests ==="

# ---------------------------------------------------------------------------
# 1. THE NEGATIVE CONTROL FOR EVERY RED BELOW
# ---------------------------------------------------------------------------
# A control that reports RED unconditionally would pass section 2 and be
# useless. So the first thing established is that on THIS box, right now, with
# its real network, the control reports UP. Without this line the reds prove
# only that something returned 1.
echo "--- 1. live GREEN (the negative control) ---"
LIVE_OUT="$(bash "$LIB" 2>&1)"; LIVE_RC=$?
LIVE_UP=0
if [ "$LIVE_RC" -eq 0 ] && printf '%s' "$LIVE_OUT" | grep -q 'POSITIVE CONTROL: UP'; then
    LIVE_UP=1
    pass "on a box with a working network the control reports UP (exit 0)"
else
    fail "the control did not report UP on this box (exit $LIVE_RC). Either this box is offline — in which case every RED below is uninterpretable, because a stuck-red control would produce the same output — or the control is broken. Output: $LIVE_OUT"
fi

printf '%s' "$LIVE_OUT" | grep -q 'self-test: PASSED' \
    && pass "the report states its own self-test result, so a reader never has to take the verdict on trust" \
    || fail "the report omitted the self-test line"

# ---------------------------------------------------------------------------
# 2. THE LOAD-BEARING ONE: RED ON A BOX WITH NO NETWORK
# ---------------------------------------------------------------------------
echo "--- 2. RED under a genuine, kernel-enforced absence of network ---"

# Returns 0 and sets NONET_* if an isolation mechanism is available.
NONET_KIND=""
if [ "$(uname -s 2>/dev/null)" = "Darwin" ] && command -v sandbox-exec >/dev/null 2>&1; then
    NONET_PROFILE='(version 1)(allow default)(deny network-outbound)(allow network-outbound (remote ip "localhost:*"))'
    # Prove the isolation itself before trusting what it produces: a profile
    # that silently failed to apply would let a healthy network through and turn
    # this whole section into a green-only demonstration wearing a red label.
    if sandbox-exec -p "$NONET_PROFILE" /usr/bin/nc -G 3 -w 3 -z 1.1.1.1 443 >/dev/null 2>&1; then
        fail "the sandbox profile did NOT actually deny off-box networking (1.1.1.1:443 still answered inside it) — section 2 cannot be run and the control is UNPROVEN in the direction that matters"
    else
        NONET_KIND="sandbox-exec"
    fi
elif command -v unshare >/dev/null 2>&1 && unshare -rn true >/dev/null 2>&1; then
    # Same check as the Darwin branch, and it is here because leaving it out
    # would be this ticket's own defect committed by its own test: an isolation
    # mechanism assumed rather than observed to work turns section 2 into a
    # green-only demonstration wearing a red label. A namespace that silently
    # kept the host's network would let 1.1.1.1 answer from inside it.
    if unshare -rn -- /bin/sh -c 'command -v nc >/dev/null 2>&1 && nc -w 3 -z 1.1.1.1 443' >/dev/null 2>&1; then
        fail "'unshare -rn' did NOT actually remove off-box networking (1.1.1.1:443 still answered inside the namespace) — section 2 cannot be run and the control is UNPROVEN in the direction that matters"
    else
        NONET_KIND="unshare"
    fi
fi

run_without_network() {  # runs "$@" with no off-box connectivity, loopback intact
    case "$NONET_KIND" in
        sandbox-exec) sandbox-exec -p "$NONET_PROFILE" "$@" ;;
        unshare)      unshare -rn -- /bin/sh -c 'ip link set lo up 2>/dev/null; exec "$@"' _ "$@" ;;
        *)            return 127 ;;
    esac
}

if [ -z "$NONET_KIND" ]; then
    fail "no way to remove this box's network was available (need sandbox-exec on Darwin or a usable 'unshare -rn' on Linux), so the RED direction of this positive control is UNPROVEN on this host. Not skipped: an unproven control is worse than none, because its green gets cited."
else
    NONET_OUT="$(run_without_network /bin/bash "$LIB" 2>&1)"; NONET_RC=$?
    if [ "$NONET_RC" -eq 1 ] && printf '%s' "$NONET_OUT" | grep -q 'POSITIVE CONTROL: DOWN'; then
        pass "RED PROVEN via $NONET_KIND: with off-box connectivity removed at the kernel, the control reports DOWN (exit 1)"
    else
        fail "the control did NOT go red on a box with no network (exit $NONET_RC via $NONET_KIND). Output: $NONET_OUT"
    fi

    # The distinction the whole design turns on. A `down` is only meaningful if
    # the instrument was demonstrably working at the moment it said so — and
    # loopback survives the condition being measured, which is why the self-test
    # can still pass here.
    printf '%s' "$NONET_OUT" | grep -q 'self-test: PASSED' \
        && pass "the RED came from a WORKING instrument — the self-test still passed on loopback, so this is a true negative and not the control breaking" \
        || fail "the RED arrived with a failed or missing self-test, which means it is indistinguishable from a broken control. Output: $NONET_OUT"

    printf '%s' "$NONET_OUT" | grep -q 'no answer' \
        && pass "the RED ships its per-target evidence, so the reader can check the verdict rather than believe it" \
        || fail "the RED carried no per-target table"

    [ "$LIVE_UP" -eq 1 ] \
        && pass "the RED is interpretable: the same control on the same box reported UP moments earlier, so it is not stuck red" \
        || fail "section 1 did not establish UP, so section 2's RED does not distinguish a working control from a stuck one"
fi

# ---------------------------------------------------------------------------
# 2b. ESTABLISHING A BLACKHOLE, RATHER THAN NAMING ONE AND HOPING
# ---------------------------------------------------------------------------
# Sections 3, 4e and 5 all need somewhere that SWALLOWS SYNs. Until mg-12aa they
# named three RFC 5737 documentation addresses and assumed the naming was the
# arranging — the same assumption section 2 already refuses to make about its
# own isolation mechanism ("prove the isolation before trusting what it
# produces"), left out of the section that needed it just as much.
#
# It is false on this host. Measured 2026-08-19 with the VPN on `utun4` holding
# the default route: 192.0.2.1:443, 198.51.100.1:443, 203.0.113.1:443 and even
# 240.0.0.1:443 — reserved Class E, routed nowhere on earth — all COMPLETE a TCP
# handshake in 0.09s, from a tunnel that terminates connect(2) locally. The
# three assertions did not detect a broken control. They reported a substrate
# that was not a blackhole, in the control's voice, and took `./build.sh` red on
# pristine main with it.
#
# So the substrate is now MEASURED before it is used, with the control's own
# probe primitive, and it must be a blackhole in both directions of the word:
# nothing answers, AND nothing answers SLOWLY. A refusal would satisfy "nothing
# answered" while testing the fast shape section 2 already covers, which would
# leave the slow shape — the mg-964e shape this section exists for — unproven
# under a passing green.
echo "--- 2b. establishing a blackhole to test against ---"

# The probe, taken from the library rather than reimplemented: the substrate has
# to be a blackhole to the SAME primitive the control will use, not to some
# other tool that might disagree about it. Echoes elapsed seconds, returns 0 if
# the endpoint ANSWERED.
bh_probe() {
    (
        # shellcheck source=/dev/null
        source "$LIB"
        netc_resolve_nc >/dev/null 2>&1 || { echo 0; exit 0; }   # "answered" => unusable
        netc_probe "$1" "$2" "$3"; rc=$?
        echo "$NETC_LAST_ELAPSED"
        exit "$rc"
    )
}

# bh_is_blackhole HOST PORT — no answer AND the probe had to be timed out for it.
# `>= 2` against a 3s budget rather than `== 3` because NETC_LAST_ELAPSED is a
# whole-second $SECONDS delta and can round down by one.
bh_is_blackhole() {
    local el rc
    el="$(bh_probe "$1" "$2" 3)"; rc=$?
    [ "$rc" -ne 0 ] || return 1
    [ "${el:-0}" -ge 2 ] || return 1
    return 0
}

BH_KIND=""
BH_TARGETS=""
BH_PROXY_NOTE=""
BH_RFC5737="192.0.2.1:443 198.51.100.1:443 203.0.113.1:443"

BH_RFC_OK=0
for t in $BH_RFC5737; do
    bh_is_blackhole "${t%:*}" "${t##*:}" && BH_RFC_OK=$(( BH_RFC_OK + 1 ))
done
if [ "$BH_RFC_OK" -eq 3 ]; then
    BH_KIND="RFC 5737 documentation addresses"
    BH_TARGETS="$BH_RFC5737"
    pass "the blackhole substrate is PROVEN before it is used: all three RFC 5737 addresses swallowed a SYN and had to be timed out"
else
    # Not a failure yet — it is a fact about the box, and the fallback below is
    # a real blackhole rather than a way around one. But it IS the control's own
    # documented false-green condition, observed, so it gets said out loud here
    # and again next to the summary.
    BH_PROXY_NOTE="  Only $BH_RFC_OK of 3 RFC 5737 documentation addresses blackholed a SYN on this box.
  Something on this host COMPLETES TCP handshakes for destinations that are
  routed nowhere, so section 1's UP is NOT evidence that anything beyond that
  something is reachable — it is the transparent-proxy false green named in
  net-control.sh's own limits section, no longer hypothetical here. Every RED
  below was still proven, against the substrate named beside it."
    echo "  NOTE — this box completes handshakes for destinations that are routed nowhere:"
    printf '%s\n' "$BH_PROXY_NOTE"

    # The fallback, and it is not a mock. A loopback listener whose accept queue
    # is full makes the kernel DROP further SYNs instead of refusing them: the
    # connect hangs until the caller's own deadline, with nothing coming back.
    # That is the same observable the mg-964e blackhole produces, made by the
    # real kernel on real sockets, and it has the property the off-box addresses
    # just lost — it cannot be answered by anything in the path, because there
    # is no path.
    if command -v python3 >/dev/null 2>&1; then
        BH_SINK_PORTFILE="$WORK/sink.ports"
        python3 - "$BH_SINK_PORTFILE" 3 >/dev/null 2>&1 <<'PY' &
import socket, sys, time
keep, ports = [], []
for _ in range(int(sys.argv[2])):
    srv = socket.socket()
    srv.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    # backlog 1 and never accept(). Measured on Darwin: with backlog 1 the
    # second connect is dropped, while backlog 0 completed six in a row — so
    # the 1 is load-bearing and not a "small number".
    srv.bind(("127.0.0.1", 0)); srv.listen(1)
    keep.append(srv)
    port = srv.getsockname()[1]
    for _i in range(8):
        c = socket.socket(); c.settimeout(0.5)
        try:
            c.connect(("127.0.0.1", port)); keep.append(c)
        except OSError:
            ports.append(port); break
with open(sys.argv[1], "w") as fh:
    fh.write(" ".join(str(p) for p in ports) if ports else "NONE")
time.sleep(600)
PY
        BH_SINK_PID=$!
        i=0
        while [ ! -s "$BH_SINK_PORTFILE" ] && [ "$i" -lt 100 ]; do sleep 0.1; i=$(( i + 1 )); done
        BH_SINK_PORTS="$(cat "$BH_SINK_PORTFILE" 2>/dev/null)"
        [ "$BH_SINK_PORTS" = "NONE" ] && BH_SINK_PORTS=""
        bh_ok=0; bh_cand=""
        for port in $BH_SINK_PORTS; do
            if bh_is_blackhole 127.0.0.1 "$port"; then
                bh_ok=$(( bh_ok + 1 )); bh_cand="$bh_cand 127.0.0.1:$port"
            fi
        done
        if [ "$bh_ok" -ge 3 ]; then
            BH_KIND="loopback SYN sinks (listeners with a full accept queue)"
            BH_TARGETS="${bh_cand# }"
            pass "a blackhole was CONSTRUCTED and proven: 3 loopback SYN sinks each swallowed a SYN and had to be timed out, so the slow shape is testable on a box whose off-box blackholes answer"
        fi
    fi
fi

if [ -z "$BH_KIND" ]; then
    fail "no blackhole could be established on this host: the RFC 5737 addresses did not swallow a SYN (they either answered or refused fast — $BH_RFC_OK of 3 blackholed), and no loopback SYN sink could be stood up either (needs python3). Sections 3, 4e and 5 cannot run, so the control's RED is UNPROVEN against dropped SYNs. Not skipped: the untested direction is the one that matters."
fi

# ---------------------------------------------------------------------------
# 3. THE SECOND REAL RED SHAPE: A BLACKHOLE, WHICH FAILS SLOW
# ---------------------------------------------------------------------------
# Section 2's denial fails instantly (EPERM). This host's actual outage does
# not: during an mg-964e DHCP blackhole the SYNs leave and nothing ever comes
# back, and an unbounded probe sits in the kernel for ~75s. A control that
# handled the fast shape and hung on the slow one would pass section 2 and be
# useless on the night it was built for.
#
# The targets come from section 2b, which PROVED them rather than named them.
# This paragraph used to assert them instead: "RFC 5737 reserves 192.0.2.0/24,
# 198.51.100.0/24 and 203.0.113.0/24 for documentation; they are routed nowhere.
# The packets here are real." Every clause of that is true of the internet and
# none of it was true of this host, where a tunnel answered all three. The
# packets are still real either way — 2b's fallback drops them in the kernel
# rather than on a wire, which is the same observable and one a routing change
# cannot take away.
echo "--- 3. RED under a real SYN blackhole (the mg-964e shape) ---"
if [ "$LIVE_UP" -eq 1 ] && [ -n "$BH_KIND" ]; then
    BH_START=$SECONDS
    BH_OUT="$(POGO_NET_CONTROL_TARGETS="$BH_TARGETS" \
              POGO_NET_CONTROL_NAME_TARGETS="" \
              POGO_NET_CONTROL_TIMEOUT=2 \
              bash "$LIB" 2>&1)"; BH_RC=$?
    BH_ELAPSED=$(( SECONDS - BH_START ))
    if [ "$BH_RC" -eq 1 ] && printf '%s' "$BH_OUT" | grep -q 'POSITIVE CONTROL: DOWN'; then
        pass "RED under dropped SYNs, not just refused ones — three blackholed addresses ($BH_KIND), verdict DOWN in ${BH_ELAPSED}s"
    else
        fail "the control did not go red against three blackholed addresses via $BH_KIND (exit $BH_RC). Output: $BH_OUT"
    fi
    # The sweep is asserted from BOTH sides, and the lower bound is the half
    # mg-12aa added. Upper: three targets at a 2s per-probe budget must not
    # approach the ~75s an unbounded SYN would cost even once, or no runner can
    # call this before alerting. Lower: three DROPPED SYNs at a 2s budget cannot
    # come back in about no time — a sweep that does was talking to a substrate
    # that REFUSED, which is the fast shape section 2 already proves and not the
    # slow one this section exists for. Without it a red is a red either way and
    # the difference is invisible.
    [ "$BH_ELAPSED" -lt 30 ] \
        && pass "the blackhole sweep stayed bounded (${BH_ELAPSED}s for 3 targets at a 2s budget), so a runner can call this before alerting" \
        || fail "the blackhole sweep took ${BH_ELAPSED}s — an unbounded probe is a control nobody can afford to call"
    [ "$BH_ELAPSED" -ge 3 ] \
        && pass "the RED came from SYNs that were DROPPED and timed out, not refused — ${BH_ELAPSED}s for 3 targets at a 2s budget, where refusals would have returned in about none" \
        || fail "the sweep returned in ${BH_ELAPSED}s, too fast for three dropped SYNs at a 2s budget: the substrate was refusing rather than blackholing, so this RED does not establish the slow shape"
elif [ "$LIVE_UP" -ne 1 ]; then
    fail "section 1 did not establish UP, so a DOWN here would carry no information — this box appears to be offline and section 3 cannot run"
else
    fail "no blackhole substrate could be established in section 2b, so the control's RED against dropped SYNs is unproven on this host"
fi

# ---------------------------------------------------------------------------
# 4. A BROKEN CONTROL MUST SAY `unknown`, NEVER `down`
# ---------------------------------------------------------------------------
# These are the failure modes of the control ITSELF. A remedy is an artifact of
# the same kind as the defect it remedies: an instrument built to stop a probe
# without a control from reporting a cause can perfectly well report a cause
# without a control of its own.
echo "--- 4. the control's own failure modes land on unknown ---"

# 4a. No primitive at all. Driven by replacing the resolver rather than by
# hiding every nc on the box, because the candidate list is hardcoded on purpose
# (a control that resolves its tools through PATH at 03:00 is the mg-015f
# mistake). What is under test is the branch, not the search.
(
    # shellcheck source=/dev/null
    source "$LIB"
    netc_resolve_nc() { NETC_NC=""; return 1; }
    net_control; rc=$?
    [ "$rc" -eq 2 ] && [ "$NET_CONTROL_VERDICT" = "unknown" ]
) && pass "no usable probe primitive => unknown (exit 2), not down" \
  || fail "a control with no primitive did not report unknown"

# 4b. The primitive can say NO but never YES — a stuck-negative nc. This is the
# exact shape that would make a control silently and permanently red, and it is
# why the self-test proves both directions instead of just the one
# pogo-deploy.sh's resolve_nc proves.
cat > "$WORK/nc-always-no" <<'EOF'
#!/bin/sh
exit 1
EOF
chmod +x "$WORK/nc-always-no"
(
    # shellcheck source=/dev/null
    source "$LIB"
    POGO_NET_CONTROL_NC="$WORK/nc-always-no"
    net_control; rc=$?
    [ "$rc" -eq 2 ] && [ "$NET_CONTROL_VERDICT" = "unknown" ] \
        && printf '%s' "$NET_CONTROL_SELFTEST" | grep -q 'FAILED'
) && pass "a primitive that can only ever say NO fails the self-test => unknown, NOT a permanent silent red" \
  || fail "a stuck-negative primitive did not fail the self-test"

# 4c. The mirror: a primitive that can only ever say YES. It would report the
# network up during a total outage, which is the false green.
cat > "$WORK/nc-always-yes" <<'EOF'
#!/bin/sh
exit 0
EOF
chmod +x "$WORK/nc-always-yes"
(
    # shellcheck source=/dev/null
    source "$LIB"
    # Two layers stop this, and both are asserted. The resolver rejects it
    # outright (it demands a definite refusal from a closed loopback port), so
    # the stub is FORCED past that layer to reach the self-test underneath —
    # otherwise the resolver would just fall through to the real nc and this
    # would be a test of nothing.
    POGO_NET_CONTROL_NC="$WORK/nc-always-yes"
    netc_resolve_nc && [ "$NETC_NC" = "$WORK/nc-always-yes" ] && exit 1  # must NOT be chosen
    netc_resolve_nc() { NETC_NC="$WORK/nc-always-yes"; NETC_NC_FLAGS=""; return 0; }
    net_control; rc=$?
    [ "$rc" -eq 2 ] && [ "$NET_CONTROL_VERDICT" = "unknown" ] \
        && printf '%s' "$NET_CONTROL_SELFTEST" | grep -q 'CLOSED loopback port as reachable'
) && pass "a primitive that can only ever say YES is refused at resolution AND caught by the self-test => unknown, never a false green" \
  || fail "a stuck-positive primitive was not caught"

# 4d. No targets. Nothing to reach establishes nothing.
(
    # shellcheck source=/dev/null
    source "$LIB"
    POGO_NET_CONTROL_TARGETS="" POGO_NET_CONTROL_NAME_TARGETS=""
    net_control; rc=$?
    [ "$rc" -eq 2 ] && [ "$NET_CONTROL_VERDICT" = "unknown" ]
) && pass "no reference targets => unknown, not down" \
  || fail "a control with no targets did not report unknown"

# 4e. Below the floor. One dead target and a dead box are the same observation,
# so one target may never produce a red.
# The target comes from section 2b's PROVEN set: "unreachable" has to be a
# measured property of the address, not a property of the RFC it was reserved by.
if [ -n "$BH_KIND" ]; then
    (
        # shellcheck source=/dev/null
        source "$LIB"
        POGO_NET_CONTROL_TARGETS="${BH_TARGETS%% *}" POGO_NET_CONTROL_NAME_TARGETS="" POGO_NET_CONTROL_TIMEOUT=2
        net_control; rc=$?
        [ "$rc" -eq 2 ] && [ "$NET_CONTROL_VERDICT" = "unknown" ] \
            && printf '%s' "$NET_CONTROL_REASON" | grep -q 'floor'
    ) && pass "a single unreachable target is below the floor => unknown, because one dead target and a dead box look identical" \
      || fail "a single unreachable target produced something other than unknown"
else
    fail "no blackhole substrate (section 2b), so the floor rule could not be exercised against an actually-unreachable target"
fi

# 4f. The report must carry the self-test line even when the control failed —
# those are precisely the runs where the reader needs to see that the verdict
# was the instrument.
(
    # shellcheck source=/dev/null
    source "$LIB"
    netc_resolve_nc() { NETC_NC=""; return 1; }
    net_control >/dev/null 2>&1
    net_control_report | grep -q 'self-test:'
) && pass "even a failed control prints its self-test line, so 'unknown' is legible as an instrument failure" \
  || fail "a failed control omitted the self-test line from its report"

# ---------------------------------------------------------------------------
# 5. THE FALSE-RED THE CONTROL CAN STILL DETECT
# ---------------------------------------------------------------------------
# Found by running the thing rather than by reasoning about it: pointed at three
# dead addresses on a healthy box, the first version reported DOWN. It was
# right about its targets and wrong about the box. The name arm answers a
# question the IP arm cannot, and reaching a host BY NAME requires both
# resolution and a completed handshake — strictly more than the empty arm.
echo "--- 5. a dead reference set does not become a dead box ---"
if [ "$LIVE_UP" -eq 1 ] && [ -n "$BH_KIND" ]; then
    FR_TARGETS="$(printf '%s' "$BH_TARGETS" | cut -d' ' -f1,2)"
    FR_OUT="$(POGO_NET_CONTROL_TARGETS="$FR_TARGETS" POGO_NET_CONTROL_TIMEOUT=2 bash "$LIB" 2>&1)"; FR_RC=$?
    if [ "$FR_RC" -eq 0 ] && printf '%s' "$FR_OUT" | grep -q 'IP reference set is what should be checked'; then
        pass "every literal-IP target dead but a name answering => UP, and the reason points at the reference set rather than the box"
    else
        fail "a dead IP reference set on a healthy box did not resolve to UP with the reference set named (exit $FR_RC). Output: $FR_OUT"
    fi

    # The distinct state that must not collapse into either verdict.
    DNS_OUT="$(POGO_NET_CONTROL_NAME_TARGETS="no-such-host-xyzzy.invalid:443" POGO_NET_CONTROL_TIMEOUT=2 bash "$LIB" 2>&1)"
    printf '%s' "$DNS_OUT" | grep -q 'dns arm:   DOWN' \
        && pass "IP-up / name-down is NAMED as name resolution, not folded into the connectivity verdict" \
        || fail "an IP-up / DNS-down box did not get its own line. Output: $DNS_OUT"
elif [ "$LIVE_UP" -ne 1 ]; then
    fail "section 1 did not establish UP, so section 5 cannot run"
else
    fail "no blackhole substrate (section 2b), so a DEAD reference set could not be arranged and section 5 cannot run"
fi

# ---------------------------------------------------------------------------
# 6. Parsing and knobs
# ---------------------------------------------------------------------------
echo "--- 6. parsing and knobs ---"

# An empty knob DISABLES an arm. This was a real bug in the first version, which
# used ${VAR:-default} and handed back the defaults to anyone who tried to turn
# an arm off — a knob that quietly ignores you.
(
    # shellcheck source=/dev/null
    source "$LIB"
    POGO_NET_CONTROL_NAME_TARGETS="" POGO_NET_CONTROL_TIMEOUT=2
    POGO_NET_CONTROL_TARGETS="192.0.2.1:443 198.51.100.1:443"
    net_control >/dev/null 2>&1
    [ "$NET_CONTROL_DNS" = "not probed" ] && [ "$NET_CONTROL_PROBED" = "2" ]
) && pass "an empty target list DISABLES that arm instead of silently restoring the defaults" \
  || fail "an emptied arm came back with its defaults"

# A malformed entry is reported and not counted as a probe, so it can never
# contribute to a red.
(
    # shellcheck source=/dev/null
    source "$LIB"
    POGO_NET_CONTROL_TARGETS="not-a-target 192.0.2.1:443" POGO_NET_CONTROL_NAME_TARGETS="" POGO_NET_CONTROL_TIMEOUT=2
    net_control >/dev/null 2>&1
    [ "$NET_CONTROL_PROBED" = "1" ] && printf '%s' "$NET_CONTROL_EVIDENCE" | grep -q 'MALFORMED'
) && pass "a malformed target is reported and NOT counted as probed, so it cannot help produce a red" \
  || fail "a malformed target was counted or went unreported"

# JSON output is parseable — this is the form a runner puts in an alert's
# structured details.
if command -v jq >/dev/null 2>&1; then
    JOUT="$(POGO_NET_CONTROL_TARGETS="192.0.2.1:443" POGO_NET_CONTROL_NAME_TARGETS="" POGO_NET_CONTROL_TIMEOUT=2 bash "$LIB" --json 2>/dev/null)"
    printf '%s' "$JOUT" | jq -e '.verdict and .selftest' >/dev/null 2>&1 \
        && pass "--json emits a parseable object carrying the verdict AND the self-test" \
        || fail "--json output was not parseable or was missing a field: $JOUT"
else
    fail "jq is not available, so the --json contract is unverified"
fi

# ---------------------------------------------------------------------------
# 7. The library does not clobber its callers
# ---------------------------------------------------------------------------
# scripts/lib/common.sh owns NC as an ANSI reset and pogo-deploy.sh owns NC and
# probe_tcp as its resolved netcat and its per-remote probe. A library that
# overwrites either breaks its caller silently, at 03:00, in the path that has
# to work when everything else is broken.
echo "--- 7. namespace hygiene ---"
(
    NC='\033[0m'
    probe_tcp() { return 42; }
    # shellcheck source=/dev/null
    source "$LIB"
    [ "$NC" = '\033[0m' ] || exit 1
    probe_tcp; [ "$?" -eq 42 ] || exit 1
) && pass "sourcing the library leaves the caller's NC and probe_tcp alone" \
  || fail "the library clobbered a caller's NC or probe_tcp"

echo
# Said again, beside the count, because this is the line a reader skims and the
# green above is the one they would otherwise quote (mg-82a6).
if [ -n "$BH_PROXY_NOTE" ]; then
    echo "NOTE — READ BEFORE CITING SECTION 1'S GREEN:"
    printf '%s\n' "$BH_PROXY_NOTE"
    echo
fi
echo "=== net-control.sh: $PASS passed, $FAIL failed ==="
[ "$FAIL" -eq 0 ]
