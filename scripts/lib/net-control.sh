#!/usr/bin/env bash
# =============================================================================
# net-control.sh — A RUNNER-SIDE POSITIVE CONTROL FOR NETWORK REACHABILITY
# =============================================================================
#
# WHAT THIS IS FOR, and it is one sentence: a probe that fails carries no
# information until something independent has established that the box could
# have reached anything at all.
#
# This fleet has now paid for that lesson three times, in three separate
# instruments, and each time the same way — a number read as a measurement from
# a probe that had no control:
#
#   * doctor's 379-probe gateway figure. Discarded: a CGNAT gateway dropping
#     ICMP as policy reads identically during an outage and during a healthy
#     minute.
#   * pogo-deploy.sh's `remedy_for_sync_class`, which tells the reader to READ
#     THE VIGIL DURATION AS A MEASUREMENT and offers it to mg-5515 as a lower
#     bound. It is a lower bound on "one host:port did not answer", which is not
#     the same statement.
#   * pogo-deploy.sh's `classify_transport` itself: it probes the deploy remote
#     and nothing else, so "this box is off the network" and "this one remote is
#     blackholed" arrive at the reader identically (mg-db96, drellem2/pogo#130).
#
# So this file is deliberately NOT part of the deploy path. It is a library any
# runner can source, or a command any runner can call:
#
#     source "$dir/net-control.sh"
#     net_control && echo "on the network" || echo "verdict: $NET_CONTROL_VERDICT"
#
#     bash net-control.sh            # human report;  exit 0 up / 1 down / 2 unknown
#     bash net-control.sh --json     # one JSON object on stdout
#
# ---------------------------------------------------------------------------
# THE THREE VERDICTS, and why the third one is the whole design
# ---------------------------------------------------------------------------
#
#   up       At least one REFERENCE TARGET answered a TCP connection. This is a
#            positive measurement. It rules the box's own connectivity out as
#            the cause of some OTHER failure, which is the only thing a caller
#            should ever use this for.
#
#   down     Every reference target was probed and NONE answered, using a
#            primitive proven — on this run, seconds earlier — able to report
#            both a yes and a no. This box is off the network, or so
#            comprehensively blackholed that the distinction has no operational
#            content.
#
#   unknown  The control could not establish either. No usable probe primitive,
#            no targets, or the instrument failed its own self-test. NOT `down`.
#
# `unknown` exists because the alternative is a control that reports RED when it
# breaks — and a control that reports RED when it breaks is worse than no
# control, because its RED gets cited. Every path in here that cannot complete
# lands on `unknown`, and every one of them says which path it was.
#
# HOW A READER TELLS A TRUE `down` FROM THE CONTROL BREAKING. They do not have
# to trust the verdict word: the control prints its own self-test result and a
# per-target table on every run, including the runs it fails. A true `down`
# shows `self-test: PASSED (loopback said yes AND said no)` above a table where
# every reference target failed. A broken control cannot show that line, because
# the line is only written when both halves of the self-test actually ran.
#
# ---------------------------------------------------------------------------
# THE SELF-TEST, which is this control's own positive control
# ---------------------------------------------------------------------------
# Before any reference target is probed, the control proves its primitive can
# answer in BOTH directions, against loopback:
#
#     YES  it starts a listener on 127.0.0.1 and the probe must reach it
#     NO   it probes a closed loopback port and the probe must not reach it
#
# Loopback is the right substrate for this because loopback survives the
# condition being measured: a box with its NIC down still routes 127.0.0.1.
# So the self-test is independent of the answer it is gating.
#
# Proving only the NO direction is what pogo-deploy.sh's `resolve_nc` does, and
# it is not enough here. A primitive that can only ever say no yields a control
# that is stuck RED — permanently, silently, and with a plausible-looking
# per-target table underneath it.
#
# ---------------------------------------------------------------------------
# WHAT THIS CONTROL CANNOT DO. Read this before quoting it.
# ---------------------------------------------------------------------------
#
#   * A CAPTIVE PORTAL OR TRANSPARENT PROXY THAT COMPLETES HANDSHAKES READS AS
#     `up`. It answers the SYN, which is all this measures. This is a real false
#     GREEN and there is no cheap fix for it at this layer; the control measures
#     "something answered", not "the internet works". Named rather than papered
#     over.
#   * IF EVERY REFERENCE TARGET IS GENUINELY DEAD, the verdict is a false `down`.
#     That is why the default set is three anycast resolvers run by three
#     different operators on three different networks, addressed by literal IP
#     so they share no resolver either. Point this at one target and you have
#     rebuilt the defect it exists to fix.
#   * IT MEASURES A LATER INSTANT than the failure that prompted it. A blip that
#     had already ended reads as `up`. The verdict is a floor, not a replay.
#   * IT SAYS NOTHING ABOUT ANY PARTICULAR REMOTE. `up` does not mean the deploy
#     remote is reachable; it means a per-remote failure is now attributable to
#     that remote instead of to this box.
#
# ---------------------------------------------------------------------------
# Knobs
# ---------------------------------------------------------------------------
#   POGO_NET_CONTROL_TARGETS      space-separated host:port reference targets
#   POGO_NET_CONTROL_NAME_TARGETS space-separated host:port probed BY NAME, for
#                                 the DNS arm (reported, never gating)
#   POGO_NET_CONTROL_TIMEOUT      per-probe seconds (default 5)
#   POGO_NET_CONTROL_MIN_TARGETS  targets that must be probed before `down` is
#                                 available at all (default 2)
#   POGO_NET_CONTROL_NC           an nc to try first, ahead of the usual paths
# =============================================================================

# Everything here is prefixed NETC_/netc_ on purpose. This file is sourced into
# runners that already own `NC` (scripts/lib/common.sh uses it for an ANSI reset,
# pogo-deploy.sh for its resolved netcat) and `probe_tcp`. A library that
# clobbers its caller's globals is a library that breaks the caller in the dark.

NETC_NC=""
NETC_NC_FLAGS=""
NETC_LAST_NOTE=""
NETC_LAST_ELAPSED=""

# Outputs. Set by net_control, read by the caller.
NET_CONTROL_VERDICT="unknown"
NET_CONTROL_REASON="net_control has not been run"
NET_CONTROL_EVIDENCE=""
NET_CONTROL_SELFTEST="not run"
NET_CONTROL_PROBED=0
NET_CONTROL_ANSWERED=0
NET_CONTROL_DNS="not run"

NETC_TIMEOUT_DEFAULT="${POGO_NET_CONTROL_TIMEOUT:-5}"
NETC_MIN_TARGETS="${POGO_NET_CONTROL_MIN_TARGETS:-2}"

# The default reference set. Three anycast resolvers, three operators
# (Cloudflare, Google, Quad9), three networks, addressed by LITERAL IP so the
# arm that establishes connectivity does not depend on name resolution. Port 443
# rather than 53 because 443 is the port least likely to be egress-filtered on a
# hotel or corporate link, and a filtered port reads as `down`.
NETC_DEFAULT_TARGETS="1.1.1.1:443 8.8.8.8:443 9.9.9.9:443"

# The DNS arm. Same operators, reached BY NAME. This is reported separately and
# never gates the verdict: an IP-layer-up / DNS-down box is a real and distinct
# state — it is what this host's DHCP outages often look like (mg-964e) — and
# collapsing it into either verdict would be this control committing the error
# it exists to prevent.
NETC_DEFAULT_NAME_TARGETS="one.one.one.one:443 dns.google:443"

# ---------------------------------------------------------------------------
# netc_resolve_nc — find a netcat, and prove it by EXECUTION rather than by path
# ---------------------------------------------------------------------------
# Proof is the NO direction only, here: port 1 on loopback is closed everywhere,
# so a working nc refuses it with exit 1 in well under a second. A missing binary
# exits 127, a non-executable one 126, and one that cannot parse these flags
# never reaches the connect — none of which is a 1.
#
# The YES direction is proven later, in netc_selftest, because it needs a
# listener and therefore needs to be able to fail for reasons that are not the
# binary's fault.
netc_resolve_nc() {
    local cand
    local -a cands=()
    # -G is macOS's CONNECT timeout, and it is not optional here: measured on
    # this host, `nc -z -w 5` alone does NOT bound the connect on Darwin — a
    # blackholed address ran 12s past it — while -G does. Linux's nc has no -G
    # and bounds connects with -w, so the flag is chosen by OS, not by parsing
    # an error string.
    if [ "$(uname -s 2>/dev/null)" = "Darwin" ]; then NETC_NC_FLAGS="-G 5"; else NETC_NC_FLAGS=""; fi
    [ -n "${POGO_NET_CONTROL_NC:-}" ] && cands+=("$POGO_NET_CONTROL_NC")
    cands+=("/usr/bin/nc" "/opt/homebrew/bin/nc" "/usr/local/bin/nc")
    cand="$(command -v nc 2>/dev/null)"
    [ -n "$cand" ] && cands+=("$cand")
    for cand in "${cands[@]}"; do
        [ -x "$cand" ] || continue
        "$cand" $NETC_NC_FLAGS -w 2 -z 127.0.0.1 1 >/dev/null 2>&1
        [ "$?" -eq 1 ] || continue
        NETC_NC="$cand"
        return 0
    done
    NETC_NC=""
    return 1
}

# ---------------------------------------------------------------------------
# netc_probe HOST PORT [TIMEOUT] — did this endpoint ANSWER?
# ---------------------------------------------------------------------------
#   0  answered   — a positive measurement, and the only thing this control
#                   treats as evidence
#   1  no answer  — anything else at all
#
# Two-valued on purpose, and this is the reason it is not `probe_tcp`. That
# function must separate "refused" from "blackholed" because it names a CAUSE,
# and separating them is hard enough that it is currently a live bug
# (drellem2/pogo#130: nc's own deadline races the watchdog, so a blackhole
# reports as a definite refusal four times in six). This control names no cause.
# It asks only whether anything answered, so the distinction that is hard is a
# distinction it does not need, and #130 cannot reach it.
#
# The connect runs in a background subshell with a killer beside it: a
# silently-dropped SYN sits in the kernel for ~75s, and a control that takes 75s
# per target is a control nobody calls from an alert path.
netc_probe() {
    local host="$1" port="$2" timeout="${3:-$NETC_TIMEOUT_DEFAULT}" p k rc start
    start=$SECONDS
    ( "$NETC_NC" $NETC_NC_FLAGS -w "$timeout" -z "$host" "$port" ) >/dev/null 2>&1 &
    p=$!
    ( sleep "$timeout"; kill -9 "$p" ) >/dev/null 2>&1 &
    k=$!
    wait "$p" 2>/dev/null; rc=$?
    kill "$k" >/dev/null 2>&1
    wait "$k" 2>/dev/null
    NETC_LAST_ELAPSED=$(( SECONDS - start ))
    case "$rc" in
        0)       NETC_LAST_NOTE="ANSWERED"; return 0 ;;
        137|143) NETC_LAST_NOTE="no answer (probe killed at ${timeout}s)"; return 1 ;;
        *)       NETC_LAST_NOTE="no answer (nc exit $rc)"; return 1 ;;
    esac
}

# ---------------------------------------------------------------------------
# netc_selftest — the control's own control. Both directions, on loopback.
# ---------------------------------------------------------------------------
# Sets NET_CONTROL_SELFTEST and returns 0 only when the primitive demonstrated,
# on this run, that it can report BOTH a reachable endpoint and an unreachable
# one. Any other outcome is a reason the verdict must be `unknown`.
netc_selftest() {
    local port base i lp ok_yes=0 ok_no=0

    # The NO half first: it needs nothing to be set up, so a failure here is
    # unambiguous.
    if netc_probe 127.0.0.1 1 2; then
        NET_CONTROL_SELFTEST="FAILED — the probe reported a CLOSED loopback port as reachable, so it cannot report a no"
        return 1
    fi
    ok_no=1

    # The YES half. Start a listener of our own and reach it. Ports are tried in
    # a small window derived from the pid so two runners do not collide, and a
    # candidate that ALREADY answers before we start anything is skipped — a
    # foreign listener would let this half pass without our listener ever having
    # bound, which is exactly the kind of pass that means nothing.
    base=$(( 40000 + ($$ % 20000) ))
    for i in 0 1 2 3 4; do
        port=$(( base + i ))
        netc_probe 127.0.0.1 "$port" 2 && continue   # occupied by someone else
        "$NETC_NC" -l "$port" >/dev/null 2>&1 &
        lp=$!
        # Give the listener a moment to bind. A tenth of a second, five times,
        # rather than one flat sleep: the common case returns immediately.
        local w=0
        while [ "$w" -lt 10 ]; do
            if netc_probe 127.0.0.1 "$port" 2; then ok_yes=1; break; fi
            sleep 0.1
            w=$(( w + 1 ))
        done
        kill "$lp" >/dev/null 2>&1
        wait "$lp" 2>/dev/null
        [ "$ok_yes" -eq 1 ] && break
    done

    if [ "$ok_yes" -ne 1 ]; then
        NET_CONTROL_SELFTEST="FAILED — could not demonstrate the probe reporting a REACHABLE endpoint (no loopback listener could be started and reached), so a 'down' from it would be indistinguishable from a stuck instrument"
        return 1
    fi

    [ "$ok_no" -eq 1 ] && [ "$ok_yes" -eq 1 ] || return 1
    NET_CONTROL_SELFTEST="PASSED (loopback said yes AND said no)"
    return 0
}

# ---------------------------------------------------------------------------
# net_control — the entry point
# ---------------------------------------------------------------------------
# Sets NET_CONTROL_VERDICT / _REASON / _EVIDENCE / _SELFTEST / _PROBED /
# _ANSWERED / _DNS. Returns 0 for `up`, 1 for `down`, 2 for `unknown` — so a
# caller that only wants the green light can write `net_control || ...`, and one
# that must not confuse red with broken reads the verdict.
net_control() {
    local t host port answered=0 probed=0 rows="" name_probed=0 name_answered=0 ip_answered=0
    local -a targets=() name_targets=()

    # Knobs are re-read HERE, per call, not captured at source time. A runner
    # sources this once at startup and calls it hours later; a knob frozen at
    # source time is a knob that silently ignores anything set in between.
    NETC_TIMEOUT_DEFAULT="${POGO_NET_CONTROL_TIMEOUT:-5}"
    NETC_MIN_TARGETS="${POGO_NET_CONTROL_MIN_TARGETS:-2}"

    NET_CONTROL_VERDICT="unknown"
    NET_CONTROL_EVIDENCE=""
    NET_CONTROL_SELFTEST="not run"
    NET_CONTROL_PROBED=0
    NET_CONTROL_ANSWERED=0
    NET_CONTROL_DNS="not run"

    if ! netc_resolve_nc; then
        NET_CONTROL_REASON="no usable netcat could be proven by execution — the control has no primitive, so it reports UNKNOWN rather than a red it cannot back"
        NET_CONTROL_SELFTEST="not run (no primitive)"
        return 2
    fi

    if ! netc_selftest; then
        NET_CONTROL_REASON="the control failed its own self-test, so its reading of the reference targets means nothing"
        return 2
    fi

    # `-` and not `:-`, deliberately: setting a target list to the empty string
    # is how a caller DISABLES an arm, and `:-` would silently hand them the
    # defaults instead. A knob that quietly ignores you is the same class of
    # defect this file exists to stop.
    read -r -a targets <<<"${POGO_NET_CONTROL_TARGETS-$NETC_DEFAULT_TARGETS}"
    if [ "${#targets[@]}" -eq 0 ]; then
        NET_CONTROL_REASON="no reference targets are configured — a control with nothing to reach cannot establish anything"
        return 2
    fi

    # ${arr[@]+...} and not "${arr[@]}": bash 3.2 — what macOS ships, and what
    # runs this at 03:00 — treats an EMPTY array's "${a[@]}" as an unbound
    # variable under `set -u` and aborts. Found by running the suite, not by
    # reading: emptying an arm crashed the control outright, which under a
    # runner's `set -u` is a control that takes the runner down with it.
    for t in ${targets[@]+"${targets[@]}"}; do
        [ -n "$t" ] || continue
        host="${t%:*}"; port="${t##*:}"
        if [ -z "$host" ] || [ -z "$port" ] || [ "$host" = "$t" ]; then
            rows="${rows}  ${t}  MALFORMED (want host:port) — not probed"$'\n'
            continue
        fi
        probed=$(( probed + 1 ))
        if netc_probe "$host" "$port" "$NETC_TIMEOUT_DEFAULT"; then
            answered=$(( answered + 1 ))
        fi
        rows="${rows}$(printf '  %-28s %-34s %ss\n' "$t" "$NETC_LAST_NOTE" "$NETC_LAST_ELAPSED")"$'\n'
    done

    ip_answered="$answered"

    # The DNS arm. Its job is to let a reader tell "off the network" from "on
    # the network, but nothing resolves", which are different tickets. It does
    # not decide the DNS question for the caller — but an answer here IS an
    # answer, and it counts toward the verdict for the reason given below.
    read -r -a name_targets <<<"${POGO_NET_CONTROL_NAME_TARGETS-$NETC_DEFAULT_NAME_TARGETS}"
    for t in ${name_targets[@]+"${name_targets[@]}"}; do
        [ -n "$t" ] || continue
        host="${t%:*}"; port="${t##*:}"
        [ -n "$host" ] && [ -n "$port" ] && [ "$host" != "$t" ] || continue
        name_probed=$(( name_probed + 1 ))
        if netc_probe "$host" "$port" "$NETC_TIMEOUT_DEFAULT"; then
            name_answered=$(( name_answered + 1 ))
        fi
        rows="${rows}$(printf '  %-28s %-34s %ss  (by name)\n' "$t" "$NETC_LAST_NOTE" "$NETC_LAST_ELAPSED")"$'\n'
    done

    probed=$(( probed + name_probed ))
    answered=$(( answered + name_answered ))
    NET_CONTROL_PROBED="$probed"
    NET_CONTROL_ANSWERED="$answered"

    if [ "$name_probed" -eq 0 ]; then
        NET_CONTROL_DNS="not probed"
    elif [ "$name_answered" -gt 0 ]; then
        NET_CONTROL_DNS="up ($name_answered of $name_probed answered by name)"
    elif [ "$ip_answered" -gt 0 ]; then
        NET_CONTROL_DNS="DOWN — literal IPs answered but no name did, which is name resolution and not connectivity"
    else
        NET_CONTROL_DNS="unknown (nothing answered by IP either, so the name arm establishes nothing on its own)"
    fi

    NET_CONTROL_EVIDENCE="$rows"

    # THE ONE RULE. Any answer, from either arm, is a positive measurement of
    # this box's connectivity, and it settles the question the control was asked.
    if [ "$answered" -gt 0 ]; then
        NET_CONTROL_VERDICT="up"
        if [ "$ip_answered" -eq 0 ]; then
            # Measured during this control's own bring-up and kept because it is
            # the false-RED failure mode made visible: pointed at three dead
            # addresses, the IP arm reported nothing reachable while the box was
            # demonstrably on the network. Reaching a target BY NAME requires
            # both resolution and a completed handshake, so it is strictly more
            # evidence than the arm that came up empty, and the reference set is
            # what is suspect — not the box.
            NET_CONTROL_REASON="no literal-IP reference target answered but $name_answered of $name_probed answered BY NAME — reaching a name needs both resolution and a handshake, so this box IS on the network and the IP reference set is what should be checked"
        else
            NET_CONTROL_REASON="$answered of $probed reference targets ANSWERED a TCP connection — this box was on the network at the time of the control"
        fi
        return 0
    fi

    if [ "$probed" -lt "$NETC_MIN_TARGETS" ]; then
        NET_CONTROL_VERDICT="unknown"
        NET_CONTROL_REASON="only $probed reference target(s) could be probed and none answered; below the floor of $NETC_MIN_TARGETS a single dead target is indistinguishable from a dead box, so this is NOT reported as down"
        return 2
    fi

    NET_CONTROL_VERDICT="down"
    NET_CONTROL_REASON="none of $probed independent reference targets answered, with a primitive that passed its own both-directions self-test seconds earlier — this box could not reach anything"
    return 1
}

# net_control_line — one line for a log. Callers that want the table read
# NET_CONTROL_EVIDENCE.
net_control_line() {
    printf 'net-control: %s — %s [self-test: %s; dns: %s]\n' \
        "$NET_CONTROL_VERDICT" "$NET_CONTROL_REASON" "$NET_CONTROL_SELFTEST" "$NET_CONTROL_DNS"
}

# net_control_report — the block a runner pastes into an alert. Always includes
# the self-test line and the per-target table, including on the runs where the
# control failed, because those are the runs where a reader most needs to see
# that the verdict was the instrument and not the network.
net_control_report() {
    printf 'NETWORK POSITIVE CONTROL: %s\n' "$(echo "$NET_CONTROL_VERDICT" | tr '[:lower:]' '[:upper:]')"
    printf '  %s\n' "$NET_CONTROL_REASON"
    printf '  self-test: %s\n' "$NET_CONTROL_SELFTEST"
    printf '  dns arm:   %s\n' "$NET_CONTROL_DNS"
    if [ -n "$NET_CONTROL_EVIDENCE" ]; then
        printf '\n%s' "$NET_CONTROL_EVIDENCE"
    fi
}

netc_json_escape() { printf '%s' "$1" | sed -e 's/\\/\\\\/g' -e 's/"/\\"/g'; }

net_control_json() {
    printf '{"verdict":"%s","reason":"%s","selftest":"%s","dns":"%s","probed":%s,"answered":%s}\n' \
        "$(netc_json_escape "$NET_CONTROL_VERDICT")" \
        "$(netc_json_escape "$NET_CONTROL_REASON")" \
        "$(netc_json_escape "$NET_CONTROL_SELFTEST")" \
        "$(netc_json_escape "$NET_CONTROL_DNS")" \
        "$NET_CONTROL_PROBED" "$NET_CONTROL_ANSWERED"
}

# Runnable as a command as well as sourceable as a library. The guard is what
# lets a test source this file and call the pieces without firing a probe sweep.
if [ "${BASH_SOURCE[0]}" = "${0}" ]; then
    net_control
    netc_rc=$?
    case "${1:-}" in
        --json) net_control_json ;;
        *)      net_control_report ;;
    esac
    exit "$netc_rc"
fi
