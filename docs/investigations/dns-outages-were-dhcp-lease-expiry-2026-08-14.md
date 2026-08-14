# The five-hour "DNS fault" was the DHCP lease expiring every 25 minutes

**Date:** 2026-08-14
**Work item:** mg-57d9 (the CAUSE ticket; mg-c058, mg-67c9, mg-516e, mg-a9d8,
mg-70f3 describe what it broke)
**Verdict:** **CAUSE ESTABLISHED.** The BT Wi-Fi hotspot's DHCP server stops
answering at lease-renewal time. macOS declares `server not responding`,
**UNBOUND**s the interface, and withdraws the entire IPv4 configuration —
address, default route and **DNS servers** — then spends ~15 minutes in DHCP
`INIT` backoff before it gets a lease back. **88 of 89 recorded failures fall
inside those windows**, against a 34.3% base rate.
**Scope note:** diagnosis only. The remedies are changes to Daniel's machine and
network and are **left for Daniel to decide** — see "What to change".

## The answer in one paragraph

The machine is on a **BT Wi-Fi public hotspot** (`captive=yes`,
`security=owe-transition`, search domain `btwifi.com`) whose DHCP lease is
**1500 s = 25 minutes**. Every lease cycle the renewal goes unanswered. At lease
expiry macOS logs `DHCP en1: status = 'server not responding'` → `UNBOUND` →
`removing 10.14.10.142`, and **11–21 ms later** (measured across all six
lease-cycle outages) `network changed: v4(en1-:10.14.10.142) DNS- Proxy-` —
**the DNS configuration is gone**. Recovery is a DHCP `INIT` with exponential backoff that takes
**~14.5 minutes**. 25 min of lease + 14.5 min of recovery = the **~40-minute
period** observed. During each recovery the box has a 169.254 link-local address,
no default route and **no resolver at all**, which is why three unrelated remotes
failed in the same minutes.

## The mechanism, verbatim from the log

Identical in every cycle. This is 2026-08-14 04:00 local (03:00Z):

```
03:58:34.613  DHCP en1: REBIND
03:58:34.614  DHCP en1: RENEW/REBIND waiting at 554.039 for 60.000000
03:59:34.622  DHCP en1: REBIND
03:59:34.622  DHCP en1: RENEW/REBIND waiting at 614.094 for 60.000000
04:00:34.632  DHCP en1: status = 'server not responding'      <-- server is silent
04:00:34.632  DHCP en1: UNBOUND
04:00:34.633  DHCP en1: removing 10.14.10.142
04:00:34.634  LINKLOCAL en1: status = 'success'               <-- falls back to 169.254
04:00:34.635  DHCP en1: INIT
04:00:34.637  DHCP en1: INIT waiting at 0 for 1.332096
04:00:34.651  network changed: v4(en1-:10.14.10.142) DNS- Proxy-   <-- DNS GONE
04:00:35.977  DHCP en1: INIT waiting at 1.34172 for 2.127055
04:00:38.114  DHCP en1: INIT waiting at 3.47873 for 4.613845
04:00:42.737  DHCP en1: INIT waiting at 8.10188 for 8.182629
   ... ~14.5 minutes of 8-second retries ...
04:15:30.008  network changed: DNS* Proxy
04:15:31.208  network changed: v4(en1!:10.14.10.142) DNS+ Proxy+ SMB   <-- DNS BACK
```

The lease parameters confirm the arithmetic — `ipconfig getpacket en1`:

```
yiaddr                  = 10.14.10.142
lease_time     (uint32) = 0x5dc      = 1500 s = 25m00s
renewal_t1_time_value   = 0x30d      =  781 s = 13m01s
router         (ip_mult)= {10.14.0.1}
domain_name_server      = {86.189.0.94}      <-- the ONLY resolver, and it comes FROM DHCP
```

**`domain_name_server` is supplied by DHCP.** That is the whole causal link:
losing the lease loses the resolver. There is exactly **one** nameserver and no
fallback, so there is nothing to fail over to.

## The eleven outages

Derived from `configd`'s own `network changed: … DNS-` / `DNS+` transitions.
Times are UTC (the log is BST, UTC+1).

| # | DNS lost | DNS back | outage | gap since prev loss |
|---|---|---|---|---|
| 1 | 01:18:36 | 01:18:46 | 0.18 m | — |
| 2 | 01:36:48 | 01:37:13 | 0.41 m | 18.2 m |
| 3 | 01:42:34 | 01:46:03 | 3.48 m | 5.8 m |
| 4 | 01:47:58 | 01:49:08 | 1.17 m | 5.4 m |
| 5 | 01:53:03 | 01:54:49 | 1.76 m | 5.1 m |
| 6 | 02:20:37 | 02:35:11 | **14.55 m** | 27.6 m |
| 7 | 03:00:34 | 03:15:31 | **14.94 m** | 39.9 m |
| 8 | 03:40:59 | 03:55:53 | **14.90 m** | 40.4 m |
| 9 | 04:21:42 | 04:36:15 | **14.54 m** | 40.7 m |
| 10 | 05:02:07 | 05:16:36 | **14.49 m** | 40.4 m |
| 11 | 05:41:59 | 05:56:56 | **14.95 m** | 39.9 m |

**Two phases.** Outages 1–5 are *roaming*: the link went `INACTIVE`, the SSID
changed and the address walked `100.118.57.151` → `10.90.99.169` → `10.90.4.236`
→ `10.14.10.142` — a different network each time. From 02:54 the box settled on
`10.14.10.142` and outages 6–11 are the **pure lease cycle**: metronomic, 39.9 –
40.7 min apart, 14.5 – 15.0 min long. **34.3% of the wall clock had no DNS.**

## Correlation with the failures the fleet recorded

Scored programmatically over `pogo events list --since=9h`, taking the `first=`
field of every `synthetic_failure_detected` (the first genuinely failing turn,
not the detection time) and every infrastructure-class `refinery_merge_failed`:

```
DNS-down windows: 11   total span 4.64h   down 95.4 min (34.3% of wall clock)
distinct failure instants scored: 89
  fell INSIDE a DNS-down window: 88/89  = 98.9%
  expected if failures were unrelated to DNS state: 34.3%
```

The single miss is `03:00:34Z` against a window opening at `03:00:34.651` — the
event log is second-granular and the window boundary is not. It is a rounding
artifact, not a counterexample; effectively **89 of 89**.

The cleanest single case is mg-72e4's merge, which the ticket already cites as
"merged on attempt 11":

| attempt | time (UTC) | |
|---|---|---|
| 1–10 | 03:00:34 → 03:14:20 | every one inside outage #7 (03:00:34 – 03:15:31) |
| 11 | ~03:16 | **merged, 29 s after DNS came back** |

## What this resolves, and what it corrects

**The two hypotheses left open at 05:38Z were neither.** It is not
per-destination resolution failure, and not sub-second global bursts. It is a
**multi-minute, global, total loss of IP configuration** — which imitates both.

- *Why it looked selective.* At 05:32:25Z a fourth destination was reachable
  while the model API failed at 05:32:41Z. Both instants sit in the **DNS-up gap
  between outages #10 and #11** (05:16:36 – 05:41:59), so neither is evidence of
  selectivity. More generally, a caller that already held an address — an open
  connection, an in-process DNS cache — sails through an outage untouched, while
  a caller doing a fresh lookup fails instantly. The apparent selectivity is by
  *whether the caller needed a lookup at that moment*, never by destination.
- *Why every probe was clean.* Two-thirds of the clock was healthy, and the
  healthy stretches ran ~25 minutes — far longer than any hand-run probe. The
  trap the ticket kept warning about was real and is now explained rather than
  merely demonstrated.
- *Why "quiet since 05:16Z" was wrong.* pm-pogo's 10-minute clean reading at
  05:25Z sat squarely inside the 25.4-minute healthy gap between #10 and #11.
  The next outage began at 05:41:59Z, on schedule.
- **The ~3-minute error spacing was the caller's retry period, not the fault's.**
  The ticket called this "the most useful clue". Those three timestamps —
  05:09:35, 05:12:25, 05:15:23 — are the **ticket's own figures**, quoted from
  the mayor's transcription and not re-derived here; what is measured here is
  that all three fall *inside the single 14.5-minute outage #10*
  (05:02:07 – 05:16:36Z), which makes them one agent retrying every ~2m50s
  against a hole that never moved. The fault has no 3-minute component; reading the
  victim's retry cadence as the fault's period is what pointed at VPN
  re-establishment.
- *Why transport-level retry survived it.* The refinery's backoff reached 120 s
  and it simply kept trying across a 15-minute hole. Correctly classed
  `infrastructure`; nothing was lost.

**Ruled out, with the evidence:**

| Hypothesis | Verdict |
|---|---|
| VPN / network-extension interference | **Ruled out, measured.** NordVPN and Cisco AnyConnect are installed and `vpnagentd` is running, but no VPN interface is ever in the path: **all 22 IPv4 service transitions in the window name `en1`** (`v4(en1-` ×11, `v4(en1!` ×11) and the string `utun` appears in `configd.log` **zero** times. Every outage begins with a DHCP timeout on `en1`, not a tunnel event. |
| mDNSResponder crash / restart | **Ruled out.** PID 208, continuously up **9 d 20 h**, zero restarts across the window. It faithfully reported having no servers to ask. |
| Upstream resolver `86.189.0.94` being broken | **Not the cause.** It answers normally whenever a lease exists. It is only ever unreachable because the box has no route to it. |
| Remote-side outage at GitHub / Google / Anthropic | **Ruled out** by the ticket's own reasoning and confirmed here — the fault is `configd` withdrawing local configuration. |

## What this does NOT establish

This ticket's whole trap is concluding from a sample that happened to land well,
and that trap applies to this diagnosis too. Stated plainly:

- **The eleven windows are a lower bound, not the complete set of bad moments.**
  They are derived from `configd`'s *configuration-level* transitions — a lease
  being lost and regained. A brief lookup timeout caused by ordinary packet loss
  on a −73 dBm link would produce no `network changed` line and would not appear
  here. So "outside a window" means "not explained by lease loss", **not**
  "nothing was wrong then".
- **One datum is genuinely unexplained**: pm-pogo's model-API failure at
  05:32:41Z sits in the healthy gap between outages #10 and #11. It never reached
  the two-failing-turn threshold that raises `synthetic_failure_detected`, so it
  appears in a transcript and not in the event log, and it is a single error
  rather than a pattern. The packet-loss reading above would cover it; so would
  an unrelated remote-side transient. **Not resolved here**, and it is the reason
  the count above is 89 events and not 90.
- **The 88/89 figure is a correlation over the event log's own records.** It is
  strong (98.9% against a 34.3% base rate) and the mechanism is independently
  established from `configd` and `ipconfig getpacket`, but nothing here
  experimentally *induced* an outage to confirm causation, and deliberately so —
  that would mean breaking Daniel's network.

## Current state — dormant, not fixed

The last outage ended **05:56:56Z**. Since then renewals have been answered
promptly (`RENEW` → `BOUND` in under 20 ms at 06:09:23, 06:21:52, 06:24:13,
06:36:42Z …).

**Outage #11 did not decay — it was interrupted.** At 05:56:40Z the link went
`INACTIVE`; at 05:56:49Z it came back and `configd` logged, in its own words:

```
05:56:49.079  en1: SSID <redacted> BSSID <redacted> NetworkID <redacted> Security NONE
05:56:49.079  en1: Wi-Fi switched networks
05:56:49.080  DHCP en1: INIT
05:56:53.958  DHCP en1: BOUND            <-- five seconds, against the usual ~14.5 minutes
```

**`Wi-Fi switched networks` is macOS's determination, not an inference here** —
and note `Security NONE`, where the network it left was `owe-transition`. What
is *not* established is whether a different DHCP **server** answered: the
address came back as the same `10.14.10.142`, and SSID/BSSID are privacy-
redacted, so the two networks may share one BT DHCP pool. What is certain is
that the five-second recovery is unlike all six lease-cycle outages, and it
coincides exactly with the network switch. **Nothing was repaired.** The box is one roam
away from the same state, and the link is both marginal and unsettled:
`configd` logged **16 `en1 link INACTIVE` and 29 `en1 link ACTIVE`** transitions
across the window, at a signal that never left `rssi −72…−74 dBm, snr 11…12 dB`.

A live probe was left running as a check that does not depend on sampling luck:
parallel sub-second lookups of four hosts down two separate resolver paths
(pure-Go straight to the nameserver, and cgo `getaddrinfo` via mDNSResponder)
plus a DNS-free TCP connect, all fired from the same instant each tick. Through
79 ticks it is clean on all nine live probes, which — consistent with everything
above — says only that the fault is currently dormant. Its ten-probe design
exists so that a recurrence is classified rather than merely noticed: `go` OK
with `sys` failing would mean mDNSResponder state; both failing means the wire;
the TCP probe failing too means the link. (`tcp/86.189.0.94:53` fails on every
tick from the first — that resolver simply does not accept TCP/53. Constant from
tick 1, so it is a flaw in the probe's choice of target, not a finding.)

## What to change — **Daniel's call, not made here**

In rough order of effect:

1. **Set explicit DNS servers on the Wi-Fi service**, so the resolver stops
   being a thing the hotspot's DHCP can revoke. This is the single highest-value
   change and it is small:
   `networksetup -setdnsservers Wi-Fi 1.1.1.1 8.8.8.8`. It does *not* fix the
   loss of address and route during `INIT`, but it removes the failure mode that
   actually generated all 89 errors — every one of them a name-resolution
   failure. Reversible with `networksetup -setdnsservers Wi-Fi Empty`.
2. **Get off the BT Wi-Fi hotspot for unattended overnight work** — Ethernet
   (`en0` is present and its service is first in the order) or a phone hotspot.
   A captive public hotspot with a 25-minute lease, a single DHCP server that
   goes silent, and −74 dBm signal is not a fit for a fleet that runs all night.
3. If neither is possible, **raise the fleet's tolerance** rather than the
   network's: the refinery already survives this by retrying, and the agent-side
   `ENOTFOUND` path is what pages a human. That is product work and belongs to
   the tickets already filed, not here.

**Not recommended:** restarting mDNSResponder (it never misbehaved), or touching
the VPN clients (they are not in the path).

## Reproducing the analysis

```bash
# NB: `log` is a zsh BUILTIN on this box -- a bare `log show` silently prints
# nothing and reads as "the window was empty". Use the absolute path.
/usr/bin/log show --start "2026-08-14 02:00:00" --end "2026-08-14 08:10:00" \
    --predicate 'process == "configd"' --style compact > configd.log

grep -E 'network changed.*DNS[-+]' configd.log      # the eleven outages
grep -E "status = 'server not responding'" configd.log   # the trigger
ipconfig getpacket en1 | grep -E 'lease_time|renewal_t1|domain_name_server'
```
