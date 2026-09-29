# Alarm routing audit: "pogod / mayor is down" alarms (2026-09-26, mg-875d)

**Rule under audit** (from the postmortem "pogod down 18 days (09-08 -> 09-26)",
forwarded by pm-pogo): no alarm whose trigger means pogod is down or unhealthy,
or the coordinator is down, may be routed **only** to the mayor or to another
agent pogod runs. In that state the recipient is gone, so the route is circular.

**Scope:** every alarm sink in `pogo` (Go daemon + `scripts/`), `pogo-reminders`,
and `~/.pogo/bin`, plus the launchd plists that set their recipients.

**Out-of-band channel.** The ticket says to use the channel defined by "the
watchdog ticket filed alongside this one". No such ticket existed when this
audit ran: every item created 2026-09-25/26 in `available/`, `claimed/`,
`done/` and `pending/` was checked, and none mentions a watchdog. So this audit
uses the out-of-band channel that already exists: **`[agents] escalation_box`**
(`config.EscalationBoxName()`, default `human`). No agent pogod runs reads that
box. Two launchd jobs do:

- `com.pogo.deadman` polls `~/.macguffin/mail/human/new` and posts a macOS
  notification for anything left unread for 900s.
- `com.pogo.notify` polls `daniel/new`, the terminal box behind the
  representative relay.

When the ticket's watchdog channel lands, it replaces this box in one place per
layer: the `escalation_box` config key for the Go side, and the recipient
arguments for the scripts.

## What was measured, and how it changes the premise

**The deploy alert was not purely circular. It was delivered, under a subject
that did not say what had happened.** `~/.pogo/reminders/deadman.log` holds
exactly one `New mail: pogo-deploy / [pogo-deploy] RED: nightly redeploy exited 6`
line per night from **2026-09-09 to 2026-09-25**. That is 17 nights. I counted
the lines; I did not re-derive the postmortem's figures. So the `human` copy
reached a macOS notification every night, through a launchd reader. The
`representative` is `auto_start = false` and was not running (`pogo agent list`,
2026-09-26 ~02:20Z), so the deadman was the reader. That route passes the rule.
The failure was the **subject line**. Exit 6 has four causes. One of them, the
`down` disposition, is "pogod is not answering". `fleet_is_down` correctly
refuses to call a bare exit 6 an outage, because the other three causes leave
the old pogod running. So pogod being down went out once a night as a routine RED, one line
in a stream that carried everything else sent to `human` (`deadman.log` shows
25, 23 and 22 notifications on 09-10, 09-17 and 09-24).

**Live config:** neither `~/.config/pogo/config.toml` nor `~/.pogo/config.toml`
sets `escalation_box`, so this box uses `human`. That is the correct setting
while the representative is not running. The pogo-reminders README says
escalations go "to `daniel` directly"; on this box that is not what happens.

**Positive control for the greps.** The ticket requires one. The deploy-script
instance is known to exist: `grep -n 'mail send' scripts/launchd/pogo-deploy.sh`
returns its `"$ALERT_TO"` and `human` sends at the `alert()` function. The same
grep over `~/.pogo/bin` returns the installed copy of that script
(`pogo-deploy.sh:1188,1190`).

## The table

Route is the default route unless noted. Circular means the rule above is
violated.

### pogod conditions (`cmd/pogod/conditions.go`, shared annunciator)

| Alarm | Trigger implies | Route before | Circular? | After mg-875d |
|---|---|---|---|---|
| A2 `scheduler_load_failed` | pogod unhealthy; no mail-check fires, so the mayor cannot be woken | mayor (+ PTY wake) | **yes** | mayor + escalation_box |
| A2 `scheduler_disabled_no_home` | same | mayor (+ wake) | **yes** | mayor + escalation_box |
| A3 `ackwatch_not_armed` | consequence of A2 on the same boot | mayor | covered | unchanged; A2 already reaches the box |
| A5 `autostart_failed:<coordinator>` | mayor never came up | mayor (the casualty) | **yes** | mayor + escalation_box |
| A5 `autostart_failed:<other>` | one crew agent missing | mayor | no | unchanged |
| A6 `restart_failed:<coordinator>` | mayor gone | mayor (the casualty) | **yes** | mayor + escalation_box |
| A6 `restart_failed:<other>` | one crew agent gone | mayor | no (mayor alive) | unchanged. The three stale rows in the ticket are about architect / pm-onethird / pm-pogo, not the mayor. They are mg-f474's never-cleared rows, not a routing fault. |
| A11 `pogod_heartbeat_write_failed` | every external check reads pogod as dead | mayor | **yes** | mayor + escalation_box |
| mg-a19a `pogod_log_not_written` | unread-pipe stderr kills pogod ~30s after boot (mg-a7a1) | mayor | **yes** | mayor + escalation_box |
| mg-5af1 `orchestration_left_stopped` | fleet, including the mayor, stopped | mayor | **yes** | mayor + escalation_box |
| A4, A7, A9, A10, A13, A14 | subsystem faults, pogod up | mayor / notify_to | no | unchanged |

Mechanism: `pogodCondition.OutOfBand` marks the row, and
`conditionAnnunciator.setOutOfBandBox(escalationBox)` names the box. A delivered
out-of-band copy counts as a delivery. This matters for A11, which raises on
every heartbeat tick: without it, a failed mayor send would mean the box got
mailed every 30s.

### pogod watchers

| Watcher | Trigger | First route | Mayor as subject | Circular? | After |
|---|---|---|---|---|---|
| firstturn | crew agent never completed a turn | mayor | whole fleet dark: box at once. **Mayor alone dark: mayor only** | **yes** | mayor + box whenever the dark set includes the addressee (`internal/firstturn/watcher.go`) |
| midsessionwedge | agent parked with an owed submit, bounded retries used up | mayor | **mayor only, no escalation** | **yes** | mayor + box when the wedged agent is the addressee |
| deafwatch | mail-check loop broken | mayor | box at once | no | unchanged |
| absentwatch | configured agent not running | mayor | box at once | no | unchanged |
| turnwatch, heartwatch | no turns / stale heartbeat | mayor | box only | no | unchanged |
| blindwatch | wedge detector blind | mayor | box | no | unchanged |
| ackwatch | fires delivered, not completed | mayor | fleet blackout: box at once. Mayor-only stall: box after 24h | partial | **not changed.** turnwatch and heartwatch reach the box within minutes for a dead mayor. Noted, not fixed. |
| stallwatch (coordinator unread mail) | mayor mail stale | mayor (nudge) | mayor only | partial | **not changed.** A deaf mayor is deafwatch's case, and deafwatch escalates at once. |
| wedgewatch | PTY frozen | **nobody** (log only) | n/a | n/a | **not changed.** Unrouted by design; blindwatch covers the detector. Noted. |
| progresswatch | fleet landing no work | mayor | box after 2h | mostly no | unchanged |
| synthwatch, refusalwatch, credexpiry, driftwatch | dead credential etc. | hard-coded `human` | never mayor | no | unchanged here. They ignored `escalation_box`, a separate gap — since closed: these four and the usage-limit coordinator now route through `escalation_box` (default `human`), drellem2/pogo#148; so does the tier-1 reaper's give-up mail (mayor + `human`, not in this table), mg-a586e. |
| ghintake, ghteardown, carrierdrift, reviewdecl, refinery | workflow | mayor / notify_to | n/a | no | unchanged |

### Scripts and launchd jobs

| Sink | Trigger implies pogod/mayor down? | Route | Circular? | After |
|---|---|---|---|---|
| `scripts/launchd/pogo-deploy.sh` `alert()` (installed `~/.pogo/bin/pogo-deploy.sh`) | exit 5/8/11/12/13, **and exit 6 `down`** | `$POGO_DEPLOY_ALERT_TO` (mayor) + `human` | **no by route**: `human` reaches the deadman (measured 17/17 nights). **Yes by subject**: exit 6 `down` said "RED" | subject `POGOD IS DOWN: nothing is answering …` and a leading banner with the bootout+bootstrap remedy, when `daemon_answering` fails **at alert time**, for any exit code. Event gains `pogod_down`. |
| same, liveness gate `daemonless` | pogod absent, no drift owed | mayor + human | no | unchanged (already says "THIS BOX HAS NO POGOD") |
| `scripts/pogo-self-deploy` `alert_external` / `deploy_stalled` reason `unknown` | pogod stopped answering mid-drain | coordinator; `human` **only if the coordinator maildir cannot be written**, which never happens with pogod down | **yes** when run outside the nightly | coordinator + `human` for `unknown`; a routine stall stays coordinator-only |
| `scripts/fleet-liveness-probe.sh` (com.pogo.fleetliveness) | no agent completed a turn | `human` | no | unchanged |
| `scripts/revision-probe.sh` (com.pogo.revisionprobe) | running revision stale | `human` | no | unchanged |
| `scripts/launchd/pogo-reclaim.sh` | disk low | `human` | no (and not installed) | unchanged |
| `scripts/launchd/pogo-recovery.sh` | n/a | sends no mail | n/a | unchanged |
| pogo-reminders `watchdog.sh` | notify/deadman/gh-issues flapping, deploy drift | `human` | no (does not watch pogod) | unchanged |
| pogo-reminders `poll-gh-issues.sh` | new GitHub issue | mayor | no (not a down alarm) | unchanged |
| `~/.pogo/bin/bridget-supervise` | bridget cannot run | mayor (`BRIDGET_ALERT_TO`) | no (bridget, not pogod) | unchanged |
| `~/.pogo/bin/vix-*` | report failed | `human` | no | unchanged |

## What was deliberately not done

- **No live config edit.** Setting `escalation_box = "daniel"` would route every
  watcher escalation past the `human` stream. Once the representative is
  running, that is what the design intends (ARCHITECTURE.md, "Escalations bypass
  by construction"). It is a deployment decision, so it is left to Daniel or
  the mayor.
- **The installed runner is not refreshed.** `~/.pogo/bin/pogo-deploy.sh` is a
  static copy. The subject fix is not live until `pogo service install-deploy`,
  and the nightly is currently held (mg-39c0 / mg-ad53).
- ackwatch's 24h mayor-only window, wedgewatch's unrouted findings, and the four
  watchers that hard-code `human` are listed above and not changed. (The last
  was later closed by drellem2/pogo#148, which routes them through
  `escalation_box`.)
