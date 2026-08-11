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
trap pogo_sandbox_down EXIT
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
# 3. THE SECOND REAL RED SHAPE: A BLACKHOLE, WHICH FAILS SLOW
# ---------------------------------------------------------------------------
# Section 2's denial fails instantly (EPERM). This host's actual outage does
# not: during an mg-964e DHCP blackhole the SYNs leave and nothing ever comes
# back, and an unbounded probe sits in the kernel for ~75s. A control that
# handled the fast shape and hung on the slow one would pass section 2 and be
# useless on the night it was built for.
#
# RFC 5737 reserves 192.0.2.0/24, 198.51.100.0/24 and 203.0.113.0/24 for
# documentation; they are routed nowhere. The packets here are real.
echo "--- 3. RED under a real SYN blackhole (the mg-964e shape) ---"
if [ "$LIVE_UP" -eq 1 ]; then
    BH_START=$SECONDS
    BH_OUT="$(POGO_NET_CONTROL_TARGETS="192.0.2.1:443 198.51.100.1:443 203.0.113.1:443" \
              POGO_NET_CONTROL_NAME_TARGETS="" \
              POGO_NET_CONTROL_TIMEOUT=2 \
              bash "$LIB" 2>&1)"; BH_RC=$?
    BH_ELAPSED=$(( SECONDS - BH_START ))
    if [ "$BH_RC" -eq 1 ] && printf '%s' "$BH_OUT" | grep -q 'POSITIVE CONTROL: DOWN'; then
        pass "RED under dropped SYNs, not just refused ones — three blackholed addresses, verdict DOWN in ${BH_ELAPSED}s"
    else
        fail "the control did not go red against three blackholed addresses (exit $BH_RC). Output: $BH_OUT"
    fi
    # Bounded, and the bound is what makes it callable from an alert path. Three
    # targets at a 2s per-probe budget must not approach the ~75s an unbounded
    # SYN would cost even once.
    [ "$BH_ELAPSED" -lt 30 ] \
        && pass "the blackhole sweep stayed bounded (${BH_ELAPSED}s for 3 targets at a 2s budget), so a runner can call this before alerting" \
        || fail "the blackhole sweep took ${BH_ELAPSED}s — an unbounded probe is a control nobody can afford to call"
else
    fail "section 1 did not establish UP, so a DOWN here would carry no information — this box appears to be offline and section 3 cannot run"
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
(
    # shellcheck source=/dev/null
    source "$LIB"
    POGO_NET_CONTROL_TARGETS="192.0.2.1:443" POGO_NET_CONTROL_NAME_TARGETS="" POGO_NET_CONTROL_TIMEOUT=2
    net_control; rc=$?
    [ "$rc" -eq 2 ] && [ "$NET_CONTROL_VERDICT" = "unknown" ] \
        && printf '%s' "$NET_CONTROL_REASON" | grep -q 'floor'
) && pass "a single unreachable target is below the floor => unknown, because one dead target and a dead box look identical" \
  || fail "a single unreachable target produced something other than unknown"

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
if [ "$LIVE_UP" -eq 1 ]; then
    FR_OUT="$(POGO_NET_CONTROL_TARGETS="192.0.2.1:443 198.51.100.1:443" POGO_NET_CONTROL_TIMEOUT=2 bash "$LIB" 2>&1)"; FR_RC=$?
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
else
    fail "section 1 did not establish UP, so section 5 cannot run"
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
echo "=== net-control.sh: $PASS passed, $FAIL failed ==="
[ "$FAIL" -eq 0 ]
